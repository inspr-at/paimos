/* PAI-946: deliberately local interactions. No account, job or mail APIs are called. */
;(() => {
  'use strict'
  const key = 'paimos:simple-prototype:v1'
  const $ = (selector) => document.querySelector(selector)
  const main = $('#main')
  const dialog = $('#dialog')
  const escape = (text) =>
    String(text).replace(
      /[&<>"']/g,
      (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char],
    )
  const icons = {
    mic: '<rect x="8" y="2" width="8" height="13" rx="4"/><path d="M5 10v2a7 7 0 0 0 14 0v-2M12 19v3m-4 0h8"/>',
    arrow: '<path d="M4 12h16m-6-6 6 6-6 6"/>',
    check: '<path d="m5 12 4 4L19 6"/>',
    sound: '<path d="m11 4-6 5H2v6h3l6 5zM16 8a6 6 0 0 1 0 8m3-11a10 10 0 0 1 0 14"/>',
    stop: '<rect x="6" y="6" width="12" height="12" rx="2"/>',
    home: '<path d="m3 10 9-7 9 7v10H3zM9 20v-7h6v7"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    edit: '<path d="m4 16 12-12 4 4-12 12H4zM13 7l4 4"/>',
    mail: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 7 9 6 9-6"/>',
  }
  const icon = (name) =>
    `<svg viewBox="0 0 24 24" aria-hidden="true">${icons[name] || icons.check}</svg>`
  const emptyState = () => ({
    version: 1,
    page: 'goal',
    goal: '',
    goalDraft: '',
    name: 'Mein Atelier',
    email: '',
    emailState: 'none',
    emailRevision: 0,
    revisions: [],
    approved: null,
    tasks: [],
    requests: [],
    changeDraft: '',
  })
  let state = emptyState()
  let restored = false
  let storageOK = true
  let viewing = null
  let pendingChange = null
  let suggestionNote = ''
  let justApproved = false
  let dialogReturn = null
  let voiceAccepted = false
  let voiceTarget = null
  let recognition = null
  let voiceGeneration = 0
  let voicePhase = 'idle'
  let voiceStartTimer = 0
  let voiceEndTimer = 0
  let voiceLimitTimer = 0
  let speech = null
  let speechGeneration = 0
  let speechTimer = 0
  function speechNotice(message) {
    const node = $('#read-state')
    if (node) node.textContent = message
    announce(message)
  }
  let previewDevice = 'computer'
  let previewDraft = { name: '', email: '', phone: '', note: '' }

  function validConfig(value) {
    return value && ['large', 'simple', 'list'].every((name) => typeof value[name] === 'boolean')
  }
  function validState(value) {
    return (
      value &&
      value.version === 1 &&
      ['goal', 'design', 'home'].includes(value.page) &&
      ['goal', 'goalDraft', 'name', 'email', 'changeDraft'].every(
        (name) => typeof value[name] === 'string' && value[name].length <= 2000,
      ) &&
      ['none', 'pending', 'confirmed', 'failed'].includes(value.emailState) &&
      Number.isSafeInteger(value.emailRevision) &&
      Array.isArray(value.revisions) &&
      value.revisions.length <= 100 &&
      value.revisions.every(
        (r) => Number.isSafeInteger(r.id) && validConfig(r.config) && typeof r.summary === 'string',
      ) &&
      (value.approved === null || value.revisions.some((r) => r.id === value.approved)) &&
      Array.isArray(value.tasks) &&
      value.tasks.length <= 100 &&
      value.tasks.every(
        (t) =>
          typeof t.id === 'string' &&
          typeof t.text === 'string' &&
          t.text.length <= 300 &&
          typeof t.done === 'boolean',
      ) &&
      Array.isArray(value.requests) &&
      value.requests.length <= 100 &&
      value.requests.every((r) => typeof r === 'string' && r.length <= 1000) &&
      (value.page === 'goal' || value.revisions.length > 0)
    )
  }
  try {
    const stored = localStorage.getItem(key)
    if (stored) {
      const parsed = JSON.parse(stored)
      if (validState(parsed)) {
        state = parsed
        restored = true
      }
    }
  } catch {
    storageOK = false
  }

  function save() {
    try {
      localStorage.setItem(key, JSON.stringify(state))
      storageOK = true
    } catch {
      storageOK = false
    }
    $('#save-state').textContent = storageOK
      ? 'Nur in diesem Browser gespeichert · keine Aufträge ausgeführt'
      : 'Speichern im Browser nicht möglich. Dieser Stand bleibt nur bis zum Schließen offen.'
  }
  function announce(message) {
    $('#announcement').textContent = message
  }
  function currentRevision() {
    return state.revisions.find((r) => r.id === viewing) || state.revisions.at(-1)
  }
  function approvedRevision() {
    return state.revisions.find((r) => r.id === state.approved)
  }
  function currentGoal() {
    return state.goal || state.goalDraft
  }
  function top() {
    $('#navigation').innerHTML = state.revisions.length
      ? `<button data-action="design" class="${state.page === 'design' ? 'active' : ''}" ${state.page === 'design' ? 'aria-current="page"' : ''}>Auftrag besprechen</button>${state.approved ? `<button data-action="home" class="${state.page === 'home' ? 'active' : ''}" ${state.page === 'home' ? 'aria-current="page"' : ''}>Mein Bereich</button>` : ''}`
      : ''
    $('#identity').textContent = state.email
      ? 'Demo-Identität · kein echtes Konto'
      : 'Gast · nur in diesem Browser'
    $('#email-state').textContent = {
      none: 'Später wiederfinden',
      pending: 'Demo: E-Mail noch offen',
      confirmed: 'Demo: E-Mail bestätigt',
      failed: 'Demo: Link nicht gesendet',
    }[state.emailState]
  }
  function render(focus = true) {
    stopMedia()
    top()
    main.innerHTML =
      state.page === 'goal' ? goalScreen() : state.page === 'design' ? designScreen() : homeScreen()
    main.dataset.screen = state.page
    save()
    if (focus) {
      main.focus({ preventScroll: true })
      window.scrollTo({ top: 0, behavior: 'instant' })
    }
  }
  function stepper(active) {
    return `<div class="stepper" aria-label="${active === 0 ? 'Ziel' : 'Entwurf'}, Schritt ${active + 1} von 3">${['Dein Wunsch', 'Deine Vorschau', 'Dein Bereich'].map((label, i) => `${i ? '<span class="step-separator" aria-hidden="true">›</span>' : ''}<span class="step ${i === active ? 'current' : i < active ? 'done' : ''}" ${i === active ? 'aria-current="step"' : ''}><span class="step-number">${i < active ? '✓' : i + 1}</span>${label}</span>`).join('')}</div>`
  }
  function composer(id, label, placeholder, value, action, submitLabel) {
    return `<form class="composer" data-form="${action}"><label for="${id}">${label}</label><textarea id="${id}" data-draft="${id}" maxlength="1000" placeholder="${placeholder}" required>${escape(value)}</textarea><div class="composer-bottom"><button type="button" class="secondary" data-action="voice" data-target="${id}">${icon('mic')}Sprechen</button><span class="voice-hint">Oder einfach tippen.</span><button type="submit" class="primary">${submitLabel}${icon('arrow')}</button></div><div id="${id}-voice" class="voice-line" role="status" aria-live="polite">Mikrofon aus. Gesprochenes bleibt vor dem Senden bearbeitbar.</div><div id="${id}-live" class="live-words" hidden></div><p id="${id}-error" class="error" role="alert" hidden></p></form>`
  }
  function goalScreen() {
    return `${restored ? '<div class="note restore">Dein lokaler Entwurf ist wieder da. Du kannst hier weitermachen.</div>' : ''}<div class="setup"><section class="setup-copy">${stepper(0)}<p class="eyebrow">Willkommen bei Paimos</p><h1>Was möchtest du<br>möglich machen?</h1><p class="lead">Beschreibe, was du brauchst. Dann schauen wir uns gemeinsam einen ersten Entwurf an.</p>${composer('goal', 'Dein Wunsch', 'Zum Beispiel: Eine Anmeldeseite für meinen Keramik-Workshop.', state.goalDraft || state.goal, 'goal', 'Entwurf ansehen ')}<span class="examples-label">Du kannst mit einem Beispiel anfangen:</span><div class="choices"><button data-action="example" data-value="Ich möchte eine Anmeldeseite für meinen Keramik-Workshop gestalten.">Workshop-Anmeldung gestalten</button></div><p class="voice-line spaced">Ohne Anmeldung ausprobieren. Diese Demo zeigt eine vorbereitete Workshop-Anmeldeseite.</p></section><aside class="setup-art-wrap" aria-label="Paimos Gestaltung"><div class="setup-art"><img src="../../assets/brand/paimos-hero.png" alt="" /></div><p class="art-caption">Ein ruhiger Ort für deine Ideen.<br>Und den nächsten guten Schritt.</p></aside></div>`
  }
  function designScreen() {
    const rev = currentRevision()
    const latest = state.revisions.at(-1)
    const previous = state.revisions.length > 1
    const old = rev.id !== latest.id
    const request = pendingChange
      ? `<div class="note" id="change-proposal"><div class="note-title">Mein Vorschlag</div><p>${escape(pendingChange.description)}</p><div class="actions"><button class="primary" data-action="apply-change">In der Vorschau zeigen</button><button data-action="discard-change">Verwerfen</button></div></div>`
      : suggestionNote
        ? `<div class="note" role="status">${escape(suggestionNote)}</div>`
        : ''
    return `${stepper(1)}<div class="workspace-heading"><div><p class="eyebrow">Dein Auftrag · gemeinsam gestalten</p><h1>So könnte deine Idee aussehen.</h1><p class="lead">Schau dir die Vorschau an. Was möchtest du ändern?</p></div><button data-action="edit-goal" class="quiet">${icon('edit')}Wunsch bearbeiten</button></div><p class="demo-explanation">Demo: Antworten, Workshop-Vorschau und Freigabe sind vorbereitet. Deine Eingaben und Änderungen bleiben lokal.</p><div class="studio"><section class="conversation" aria-labelledby="conversation-title"><div class="speaker"><img src="../../assets/brand/paimos-logo.svg" alt="" />Dein Gespräch mit Paimos</div><h2 id="conversation-title">Von deinem Wunsch zum Entwurf.</h2><p id="reply">Dein Wunsch: „${escape(state.goal)}“<br><br>Wir probieren das hier an einer Workshop-Anmeldung aus. Sie zeigt den Termin, erklärt den Kurs und nimmt Anmeldedaten auf. Das Formular kannst du direkt testen.</p><button class="read-button" data-action="read" data-target="reply">${icon('sound')}Antwort vorlesen</button><p id="read-state" class="voice-line" role="status"></p>${composer('change', 'Was möchtest du ändern?', 'Zum Beispiel: Das Formular kürzer, bitte.', state.changeDraft, 'change', 'Besprechen ')}<div class="choices" aria-label="Änderungen ausprobieren"><button data-action="suggest" data-change="simple">Formular kürzer</button><button data-action="suggest" data-change="list">Termin hervorheben</button><button data-action="suggest" data-change="large">Größere Schrift</button></div>${request}${state.requests.length ? `<details class="history"><summary>Deine Änderungswünsche (${state.requests.length})</summary><ol>${state.requests.map((r) => `<li>${escape(r)}</li>`).join('')}</ol></details>` : ''}</section><section class="preview-column" aria-label="Vorschau deiner Workshop-Anmeldeseite"><div class="preview-shell"><div class="preview-toolbar"><strong>Version ${rev.id}${old ? ' · vorherige Fassung' : ''}</strong><div class="device-controls" aria-label="Vorschaugröße"><button data-action="device" data-device="computer" aria-pressed="${previewDevice === 'computer'}">Computer</button><button data-action="device" data-device="phone" aria-pressed="${previewDevice === 'phone'}">Handy</button></div></div><div class="device-stage ${previewDevice === 'phone' ? 'phone' : ''}">${preview(rev.config)}</div></div><div class="preview-meta"><span><strong>Was sich geändert hat</strong><br>${escape(rev.summary)}</span><div class="actions">${previous ? `<button data-action="compare">${old ? 'Aktuelle Version zeigen' : 'Vorher vergleichen'}</button>` : ''}</div></div><div class="approval-bar"><div><strong>Diese Version passt für dich?</strong><p>Deine Entscheidung gilt genau für Version ${rev.id}.</p></div><button class="primary" data-action="approve">Version ${rev.id} freigeben ${icon('check')}</button></div>${state.approved ? '<button class="quiet spaced" data-action="home">Zurück zu meinem Bereich</button>' : ''}<p class="voice-line">Eine Freigabe veröffentlicht keine Seite. Die echte Umsetzung braucht später eine eingerichtete Verbindung.</p></section></div>`
  }
  function preview(config) {
    return `<div class="preview workshop ${config.large ? 'large-type' : ''} ${config.simple ? 'short-form' : ''} ${config.list ? 'date-emphasis' : ''}" data-preview-config="${config.large ? 'large' : 'normal'}-${config.simple ? 'short' : 'full'}-${config.list ? 'emphasis' : 'quiet'}"><div class="workshop-brand">ATELIER <span>TON & ZEIT</span></div><div class="workshop-intro"><div><p class="workshop-kicker">Keramik für Neugierige</p><h2>Ein Morgen<br>mit Ton.</h2><p>Raus aus dem Kopf, rein in die Hände. Forme deine erste Schale – ganz ohne Vorkenntnisse.</p></div><div class="ceramic-art" aria-hidden="true"><span class="clay-slab"></span><span class="clay-bowl"></span><span class="clay-circle"></span></div></div><div class="workshop-date"><span>Samstag · 10–13 Uhr</span><span>Atelier am Park · 6 Plätze</span></div>${!config.simple ? '<p class="workshop-description">Ton, Werkzeug und ein gemeinsamer Kaffee sind dabei. Nach dem Brennen kannst du dein Stück abholen.</p>' : ''}<form class="signup-form" data-form="preview"><h3>Dein Platz am Werktisch</h3><p class="preview-fixture">Beispielangebot · keine echte Buchung</p><div class="signup-grid"><div><label for="preview-name">Dein Name</label><input class="input" id="preview-name" data-preview-field="name" autocomplete="off" maxlength="80" value="${escape(previewDraft.name)}" required placeholder="Vorname" /></div><div><label for="preview-email">Deine E-Mail</label><input class="input" id="preview-email" data-preview-field="email" type="email" autocomplete="off" maxlength="254" value="${escape(previewDraft.email)}" required placeholder="du@beispiel.at" /></div>${!config.simple ? `<div><label for="preview-phone">Telefon (optional)</label><input class="input" id="preview-phone" data-preview-field="phone" type="tel" autocomplete="off" maxlength="50" value="${escape(previewDraft.phone)}" placeholder="Für Rückfragen" /></div><div><label for="preview-note">Deine Nachricht (optional)</label><input class="input" id="preview-note" data-preview-field="note" maxlength="300" value="${escape(previewDraft.note)}" placeholder="Was möchtest du uns sagen?" /></div>` : ''}</div><button class="primary" type="submit">Platz vormerken ${icon('arrow')}</button><p id="preview-result" class="preview-fixture" role="status">Du kannst das Formular testen. Es wird nichts versendet.</p></form></div>`
  }
  function homeScreen() {
    const rev = approvedRevision()
    if (!rev) {
      state.page = 'design'
      return designScreen()
    }
    const done = state.tasks.filter((t) => t.done).length
    return `<div>${justApproved ? `<div class="success-note">${icon('check')}<div>Version ${rev.id} deiner Workshop-Seite ist freigegeben.<small>In dieser Demo festgehalten. Die Seite wurde nicht veröffentlicht.</small></div></div>` : ''}<div class="workspace-heading"><div><p class="eyebrow">${escape(state.name)}</p><h1>Was ist als Nächstes dran?</h1><p class="lead">Dein Ziel: ${escape(state.goal)}</p></div><button data-action="design">${icon('edit')}Auftrag weiter besprechen</button></div><div class="result-card"><div><p class="eyebrow">Dein Auftrag</p><h2>Workshop-Anmeldung</h2><p>Version ${rev.id} freigegeben · wartet auf Einrichtung</p><small>Verbindung unbekannt. In dieser Demo wird kein Auftrag gestartet.</small></div><div class="actions"><button class="primary" data-action="design">Vorschau ansehen</button><button data-action="connection">Einrichtung ansehen</button></div></div><div class="home-layout"><section class="home-card" aria-labelledby="tasks-title"><h2 id="tasks-title">Deine nächsten Schritte</h2><p class="lead">Eine Sache nach der anderen.</p>${state.tasks.length ? `<p class="count-label">${state.tasks.length - done} offen · ${done} erledigt · nur lokale Aufgaben</p><div class="task-list">${state.tasks.map((t) => `<div class="task-row ${t.done ? 'done' : ''}"><input type="checkbox" id="task-${escape(t.id)}" data-task="${escape(t.id)}" ${t.done ? 'checked' : ''}/><label for="task-${escape(t.id)}">${escape(t.text)}</label></div>`).join('')}</div>` : `<div class="empty"><div class="icon-tile">${icon('plus')}</div><strong>Hier ist Platz für deinen ersten Schritt.</strong>Füge eine Aufgabe hinzu. Sie bleibt in diesem Browser.</div>`}${composer('task', 'Deine nächste Aufgabe', 'Was möchtest du als Erstes erledigen?', '', 'task', 'Hinzufügen ')}</section><aside class="home-side"><div class="home-card"><p class="eyebrow">Dein roter Faden</p><h2>Darum geht es dir.</h2><p>${escape(state.goal)}</p><button class="quiet" data-action="edit-goal">Wunsch ändern</button></div><div class="home-card"><h2>Dein Bereich bleibt deiner.</h2><p>Du entscheidest, wann aus deinem Entwurf ein echter Auftrag wird.</p><button class="quiet" data-action="rename">Namen ändern</button></div></aside></div></div>`
  }

  function modal(title, content) {
    stopMedia()
    dialogReturn = document.activeElement
    dialog.innerHTML = `<div class="dialog-head"><h2 id="dialog-title">${title}</h2><button class="quiet" data-action="close" aria-label="Dialog schließen">✕</button></div>${content}`
    if (!dialog.open) dialog.showModal()
  }
  function closeDialog() {
    dialog.close()
    if (dialogReturn?.isConnected) dialogReturn.focus()
  }
  function emailDialog() {
    modal(
      'Später wiederfinden',
      `<p>Im fertigen Produkt sichert ein E-Mail-Link deinen Zugang. Hier probierst du den Ablauf aus – es wird keine E-Mail versendet.</p><form data-form="email"><label for="email-input">Deine E-Mail</label><input id="email-input" class="input" type="email" autocomplete="email" maxlength="254" value="${escape(state.email)}" required placeholder="du@beispiel.at"/><p class="voice-line">Auch ohne Bestätigung kannst du hier weitergestalten.</p><div class="actions"><button type="submit" class="primary">Linkversand simulieren</button><button type="button" data-action="close">Weiter ausprobieren</button></div></form>${state.email ? `<div class="note"><div class="note-title">Demo-Zustand</div><p>${{ pending: 'Bestätigung offen. Dein Entwurf bleibt nutzbar.', confirmed: 'Bestätigung simuliert. Es besteht weiterhin kein echtes Konto.', failed: 'Versand fehlgeschlagen. Du kannst erneut versuchen oder weiterarbeiten.', none: 'Noch kein Link angefordert.' }[state.emailState]}</p><div class="choices">${state.emailState === 'pending' ? '<button data-action="email-link">Demo-Link öffnen</button><button data-action="email-fail">Versandfehler zeigen</button>' : ''}${state.emailState === 'confirmed' ? '<button data-action="email-expire">Abgelaufenen Link zeigen</button>' : ''}</div></div>` : ''}`,
    )
  }
  function propose(type) {
    const config = { ...state.revisions.at(-1).config }
    if (config[type]) {
      pendingChange = null
      suggestionNote = 'Diese Änderung ist in der aktuellen Vorschau bereits enthalten.'
      render(false)
      announce(suggestionNote)
      return
    }
    const descriptions = {
      large:
        'Ich vergrößere die Schrift in der Workshop-Seite und im Formular. Die Inhalte bleiben gleich.',
      simple:
        'Ich kürze die Anmeldung auf Name und E-Mail. Telefon, Nachricht und der zusätzliche Erklärungstext entfallen.',
      list: 'Ich hebe Termin und Ort in einer ruhigen Aqua-Fläche hervor. So sieht man gleich, wann und wo der Workshop stattfindet.',
    }
    const titles = {
      large: 'Größere Schrift',
      simple: 'Formular auf Name und E-Mail gekürzt',
      list: 'Termin und Ort hervorgehoben',
    }
    config[type] = true
    pendingChange = { type, config, title: titles[type], description: descriptions[type] }
    suggestionNote = ''
    state.changeDraft = ''
    if (state.requests.length < 100) state.requests.push(titles[type])
    render(false)
    $('#change-proposal')?.scrollIntoView({ block: 'nearest', behavior: 'instant' })
    announce('Änderung vorgeschlagen. Mit In der Vorschau zeigen anwenden.')
  }
  function inputError(id, message) {
    const node = $(`#${id}-error`)
    if (node) {
      node.hidden = false
      node.textContent = message
    }
    $(`#${id}`)?.focus()
  }
  function updateGoal(goal) {
    state.goal = goal
    state.goalDraft = goal
    if (!state.revisions.length)
      state.revisions.push({
        id: 1,
        config: { large: false, simple: false, list: false },
        summary:
          'Erster vorbereiteter Entwurf · Workshop-Informationen und Anmeldung mit vier Feldern',
      })
  }
  function addRevision(config, summary) {
    if (state.revisions.length >= 100) {
      announce('Die Demo hat 100 Entwürfe erreicht. Bitte beginne eine neue Demo.')
      return false
    }
    state.revisions.push({ id: state.revisions.at(-1).id + 1, config, summary })
    viewing = null
    return true
  }

  document.addEventListener('submit', (event) => {
    const form = event.target.closest('[data-form]')
    if (!form) return
    event.preventDefault()
    const kind = form.dataset.form
    if (kind === 'goal') {
      const goal = $('#goal').value.trim()
      if (!goal) return inputError('goal', 'Ein kurzer Wunsch reicht, damit wir anfangen können.')
      updateGoal(goal)
      state.page = 'design'
      restored = false
      render()
    } else if (kind === 'change') {
      const text = $('#change').value.trim()
      if (!text) return inputError('change', 'Beschreibe kurz, was du ändern möchtest.')
      // Only the three explicit demonstrations are applied. Free text is never fake execution.
      if (/^(bitte )?(größere schrift|schrift größer)[.!]?$/iu.test(text)) return propose('large')
      if (
        /^(bitte )?(das )?(formular kürzer|formular kürzen|nur name und e-mail)(,? bitte)?[.!]?$/iu.test(
          text,
        )
      )
        return propose('simple')
      if (/^(bitte )?(termin hervorheben|termin größer)[.!]?$/iu.test(text)) return propose('list')
      if (state.requests.length < 100) state.requests.push(text)
      state.changeDraft = ''
      pendingChange = null
      suggestionNote =
        'Dein Wunsch ist in dieser Demo festgehalten. Frei formulierte Änderungen werden hier noch nicht umgesetzt. Mit den drei Vorschlägen kannst du eine sichtbare Änderung ausprobieren.'
      render(false)
      announce(suggestionNote)
    } else if (kind === 'task') {
      const text = $('#task').value.trim()
      if (!text) return inputError('task', 'Gib deiner Aufgabe einen kurzen Namen.')
      if (text.length > 300)
        return inputError('task', 'Bitte beschränke die Aufgabe auf 300 Zeichen.')
      if (state.tasks.length >= 100)
        return inputError('task', 'In dieser Demo sind bis zu 100 Aufgaben möglich.')
      state.tasks.push({
        id: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
        text,
        done: false,
      })
      justApproved = false
      render(false)
      $('#task').focus()
      announce('Aufgabe lokal hinzugefügt.')
    } else if (kind === 'preview') {
      $('#preview-result').textContent =
        'Demo erfolgreich ausprobiert. Keine Anmeldung gesendet, kein Platz gebucht.'
    } else if (kind === 'email') {
      const email = $('#email-input').value.trim()
      state.email = email
      state.emailRevision += 1
      state.emailState = 'pending'
      save()
      top()
      emailDialog()
      announce('Demo: Bestätigung ausstehend. Es wurde keine E-Mail gesendet.')
    } else if (kind === 'rename') {
      state.name = $('#name-input').value.trim() || 'Mein Bereich'
      closeDialog()
      render(false)
      announce('Bereich lokal umbenannt.')
    } else if (kind === 'edit-goal') {
      const goal = $('#goal-edit').value.trim()
      if (!goal) return
      updateGoal(goal)
      closeDialog()
      render(false)
      announce('Wunsch geändert. Die vorbereitete Workshop-Vorschau bleibt unverändert.')
    }
  })

  document.addEventListener('click', (event) => {
    const button = event.target.closest('[data-action]')
    if (!button || button.disabled) return
    const action = button.dataset.action
    if (action === 'close') return closeDialog()
    if (action === 'example') {
      $('#goal').value = button.dataset.value
      state.goalDraft = button.dataset.value
      save()
      $('#goal').focus()
      return
    }
    if (action === 'suggest') return propose(button.dataset.change)
    if (action === 'apply-change' && pendingChange) {
      if (!addRevision(pendingChange.config, pendingChange.title)) return
      const summary = pendingChange.title
      pendingChange = null
      suggestionNote = `${summary} ist im neuen Entwurf sichtbar. Vergleiche ihn, bevor du ihn übernimmst.`
      render(false)
      announce(suggestionNote)
      return
    }
    if (action === 'discard-change') {
      pendingChange = null
      suggestionNote = 'Vorschlag verworfen. Dein Entwurf bleibt wie zuvor.'
      render(false)
      return
    }
    if (action === 'compare') {
      viewing = viewing ? null : state.revisions.at(-2)?.id
      render(false)
      announce(`Entwurf ${currentRevision().id} sichtbar.`)
      return
    }
    if (action === 'device') {
      previewDevice = button.dataset.device
      render(false)
      return
    }
    if (action === 'approve') {
      if (pendingChange) {
        announce(
          'Bitte zeige den offenen Änderungsvorschlag zuerst in der Vorschau oder verwirf ihn.',
        )
        $('#change-proposal')?.scrollIntoView({ block: 'center' })
        return
      }
      state.approved = currentRevision().id
      state.page = 'home'
      justApproved = true
      render()
      announce(`Version ${state.approved} lokal freigegeben. Keine Veröffentlichung.`)
      return
    }
    if (action === 'design') {
      state.page = 'design'
      viewing = null
      justApproved = false
      render()
      return
    }
    if (action === 'home') {
      state.page = 'home'
      justApproved = false
      render()
      return
    }
    if (action === 'rename')
      return modal(
        'Wie soll dein Bereich heißen?',
        `<form data-form="rename"><label for="name-input">Name deines Bereichs</label><input id="name-input" class="input" maxlength="60" value="${escape(state.name)}"/><div class="actions"><button type="submit" class="primary">Namen übernehmen</button><button type="button" data-action="close">Abbrechen</button></div></form>`,
      )
    if (action === 'edit-goal')
      return modal(
        'Was möchtest du leichter machen?',
        `<form data-form="edit-goal"><label for="goal-edit">Dein Ziel</label><textarea id="goal-edit" class="input" rows="4" maxlength="1000" required>${escape(currentGoal())}</textarea><p>Hier änderst du deinen roten Faden. Deine Aufgaben und die gewählte Ansicht bleiben erhalten.</p><div class="actions"><button type="submit" class="primary">Ziel übernehmen</button><button type="button" data-action="close">Abbrechen</button></div></form>`,
      )
    if (action === 'connection')
      return modal(
        'Verbindung zu deinem Computer',
        '<p><strong>Noch nicht eingerichtet · Zustand unbekannt.</strong></p><p>Zum Besprechen und Prüfen brauchst du keine Verbindung. Für echte Arbeit richtet deine technische Ansprechperson später den Zugang ein.</p><details class="history"><summary>Hinweis für deine technische Ansprechperson</summary><p>In der vollständigen Paimos-Einrichtung muss sie den Computer und den vorgesehenen Arbeitsbereich prüfen, den Zugang einrichten und die bestätigte Verbindung ansehen. Diese Demo verbindet nichts und enthält keinen ausführbaren Einrichtungsbefehl.</p></details><div class="actions"><button class="primary" data-action="close">Weiter am Entwurf arbeiten</button></div>',
      )
    if (action === 'email') return emailDialog()
    if (action === 'email-fail') {
      state.emailState = 'failed'
      save()
      top()
      emailDialog()
      return
    }
    if (action === 'email-expire')
      return modal(
        'Dieser Demo-Link ist abgelaufen',
        '<p>Dein Arbeitsstand ist weiter da. Du kannst einen neuen Link ausprobieren oder direkt weiterarbeiten.</p><div class="actions"><button class="primary" data-action="email">Neuen Link ausprobieren</button><button data-action="close">Weiterarbeiten</button></div>',
      )
    if (action === 'email-link')
      return modal(
        'E-Mail bestätigen',
        `<p>Demo-Link für <strong>${escape(state.email)}</strong>. Erst dieser Klick simuliert die Bestätigung. Es entsteht kein echtes Konto.</p><div class="actions"><button class="primary" data-action="email-confirm" data-revision="${state.emailRevision}">Bestätigung simulieren</button><button data-action="close">Später</button></div>`,
      )
    if (action === 'email-confirm') {
      if (Number(button.dataset.revision) !== state.emailRevision || state.emailState !== 'pending')
        return modal(
          'Link nicht mehr gültig',
          '<p>Die Adresse oder der Link hat sich geändert. Du kannst weiterhin an deinem Entwurf arbeiten.</p><div class="actions"><button data-action="email">Neuen Link ausprobieren</button></div>',
        )
      state.emailState = 'confirmed'
      save()
      top()
      closeDialog()
      announce('E-Mail-Bestätigung simuliert. Dein Arbeitsstand bleibt erhalten.')
      return
    }
    if (action === 'about')
      return modal(
        'Ein kleiner Einblick in Paimos',
        `<p>Dieser Klickprototyp zeigt einen möglichen einfachen Arbeitsablauf: Wunsch beschreiben, eine Workshop-Anmeldeseite besprechen, eine genaue Version freigeben und lokale Aufgaben festhalten.</p><ul class="check-list"><li>${icon('check')}Vorschau und Aufgaben reagieren auf deine Eingaben.</li><li>${icon('check')}Die drei Änderungen an der Workshop-Seite sind vorbereitet. Freie Wünsche werden nur festgehalten.</li><li>${icon('check')}Antworten und E-Mail-Ablauf sind als Demo gekennzeichnet.</li></ul><p>Es laufen keine bezahlten Paimos-Aufträge. Nur auf deinen Klick kann die Sprachfunktion deines Browsers aktiv werden. Der Browser kann Sprache an seinen Anbieter senden.</p><p>Deine Eingaben bleiben in diesem Browser. „Demo neu beginnen“ löscht ausschließlich diese Demo-Daten.</p><div class="link-list"><a href="./codex-style-study.png" target="_blank" rel="noopener">Visuelle Studie ansehen</a></div><div class="actions"><button class="primary" data-action="close">Weiter ausprobieren</button></div>`,
      )
    if (action === 'reset')
      return modal(
        'Demo neu beginnen?',
        '<p>Dadurch entfernst du nur die Ziele, Entwürfe und Aufgaben dieses Klickprototyps aus diesem Browser.</p><div class="actions"><button class="primary" data-action="confirm-reset">Demo-Daten entfernen</button><button data-action="close">Behalten</button></div>',
      )
    if (action === 'confirm-reset') {
      state = emptyState()
      previewDraft = { name: '', email: '', phone: '', note: '' }
      restored = false
      viewing = null
      pendingChange = null
      suggestionNote = ''
      justApproved = false
      closeDialog()
      render()
      return
    }
    if (action === 'voice') {
      if (recognition) return finishCapture()
      voiceTarget = button.dataset.target
      if (!(window.SpeechRecognition || window.webkitSpeechRecognition))
        return voiceMessage(
          'Dieser Browser unterstützt hier keine Spracheingabe. Du kannst dieselbe Aufgabe einfach tippen.',
          true,
        )
      if (voiceAccepted) return startCapture()
      return modal(
        'Mit deiner Stimme schreiben',
        `<p>Dein Browser wandelt Sprache in Text um. Er kann dafür Audio an seinen Anbieter senden. Paimos ruft in dieser Demo keinen bezahlten Sprachdienst auf.</p><p>Nach deinem Klick fragt der Browser nach dem Mikrofon. Du siehst die erkannten Wörter und sendest erst, wenn sie passen. Tippen ist immer möglich.</p><div class="actions"><button class="primary" data-action="voice-start">Mikrofon einschalten</button><button data-action="close">Lieber tippen</button></div>`,
      )
    }
    if (action === 'voice-start') {
      voiceAccepted = true
      closeDialog()
      return startCapture()
    }
    if (action === 'read') return readAloud(button.dataset.target)
  })

  document.addEventListener('input', (event) => {
    const input = event.target
    if (input.matches('[data-preview-field]'))
      previewDraft[input.dataset.previewField] = input.value
    if (input.matches('[data-draft]')) {
      if (recognition && input.id === voiceTarget)
        abortCapture('Mikrofon aus. Du kannst deinen Text bearbeiten und senden.')
      if (input.id === 'goal') state.goalDraft = input.value
      if (input.id === 'change') state.changeDraft = input.value
      save()
    }
  })
  document.addEventListener('change', (event) => {
    if (!event.target.matches('[data-task]')) return
    const task = state.tasks.find((item) => item.id === event.target.dataset.task)
    if (!task) return
    task.done = event.target.checked
    save()
    // Update in place so keyboard focus stays on the same checkbox.
    event.target.closest('.task-row').classList.toggle('done', task.done)
    const done = state.tasks.filter((item) => item.done).length
    $('.count-label').textContent =
      `${state.tasks.length - done} offen · ${done} erledigt · nur lokale Aufgaben`
    announce(
      task.done
        ? 'Aufgabe als erledigt markiert. Erneuter Klick macht das rückgängig.'
        : 'Aufgabe wieder geöffnet.',
    )
  })
  document.addEventListener('keydown', (event) => {
    if ((event.ctrlKey || event.metaKey) && event.key === 'Enter' && event.target.closest('form')) {
      event.preventDefault()
      event.target.closest('form').requestSubmit()
    }
  })

  function voiceMessage(message, error = false) {
    const node = $(`#${voiceTarget}-voice`)
    if (node) {
      node.textContent = message
      node.classList.toggle('error', error)
    }
  }
  function paintVoice() {
    const button = $(`[data-action="voice"][data-target="${voiceTarget}"]`)
    if (!button) return
    const active = voicePhase !== 'idle'
    button.innerHTML = `${icon(active ? 'stop' : 'mic')}${active ? 'Mikrofon stoppen' : 'Sprechen'}`
    button.classList.toggle('recording', active)
    button.setAttribute('aria-pressed', String(active))
  }
  function clearVoiceTimers() {
    clearTimeout(voiceStartTimer)
    clearTimeout(voiceEndTimer)
    clearTimeout(voiceLimitTimer)
  }
  function abortCapture(message = 'Mikrofon aus. Du kannst weiter tippen.') {
    ++voiceGeneration
    clearVoiceTimers()
    const old = recognition
    recognition = null
    voicePhase = 'idle'
    if (old) {
      try {
        old.abort()
      } catch {
        /* Already ended by the browser. */
      }
    }
    const live = $(`#${voiceTarget}-live`)
    if (live) {
      live.hidden = true
      live.textContent = ''
    }
    paintVoice()
    voiceMessage(message)
  }
  function finishCapture() {
    if (!recognition) return
    if (voicePhase === 'starting' || voicePhase === 'stopping') return abortCapture()
    voicePhase = 'stopping'
    paintVoice()
    voiceMessage('Mikrofon wird gestoppt. Die letzten erkannten Wörter werden übernommen.')
    try {
      recognition.stop()
    } catch {
      return abortCapture()
    }
    voiceEndTimer = window.setTimeout(() => abortCapture(), 1800)
  }
  function startCapture() {
    stopPlayback()
    const input = $(`#${voiceTarget}`)
    if (!input) return
    const Recognition = window.SpeechRecognition || window.webkitSpeechRecognition
    if (!Recognition)
      return voiceMessage('Spracheingabe ist hier nicht verfügbar. Bitte tippe deinen Text.', true)
    abortCapture()
    const generation = ++voiceGeneration
    let receivedWords = false
    let failed = false
    let instance
    try {
      instance = new Recognition()
    } catch {
      return voiceMessage('Spracheingabe konnte nicht gestartet werden. Bitte tippe weiter.', true)
    }
    recognition = instance
    voicePhase = 'starting'
    paintVoice()
    instance.lang = 'de-DE'
    instance.continuous = true
    instance.interimResults = true
    voiceMessage('Mikrofon wird angefragt. Du kannst jederzeit stoppen.')
    const current = () => generation === voiceGeneration && recognition === instance
    instance.onstart = () => {
      if (!current()) return
      clearTimeout(voiceStartTimer)
      voicePhase = 'listening'
      paintVoice()
      voiceMessage(
        'Mikrofon an. Die erkannten Wörter erscheinen hier. Zum Beenden auf Stoppen klicken.',
      )
      voiceLimitTimer = window.setTimeout(() => {
        if (current()) finishCapture()
      }, 60000)
    }
    instance.onresult = (event) => {
      if (!current() || !input.isConnected) return
      let interim = ''
      for (let i = event.resultIndex; i < event.results.length; i += 1) {
        const words = event.results[i][0].transcript.trim()
        if (event.results[i].isFinal && words) {
          const limit = input.maxLength > 0 ? input.maxLength : 1000
          const start = input.selectionStart ?? input.value.length
          const end = input.selectionEnd ?? start
          const before = input.value.slice(0, start)
          const after = input.value.slice(end)
          const prefix = before && !/\s$/.test(before) ? ' ' : ''
          const suffix = after && !/^\s|^[,.;:!?]/.test(after) ? ' ' : ''
          const room = Math.max(0, limit - before.length - after.length)
          const inserted = `${prefix}${words}${suffix}`.slice(0, room)
          input.value = before + inserted + after
          input.setSelectionRange(start + inserted.length, start + inserted.length)
          receivedWords = true
          if (input.id === 'goal') state.goalDraft = input.value
          if (input.id === 'change') state.changeDraft = input.value
          save()
        } else interim += `${words} `
      }
      const live = $(`#${voiceTarget}-live`)
      if (live) {
        live.textContent = interim ? `Gerade erkannt: ${interim}` : ''
        live.hidden = !interim
      }
    }
    instance.onerror = (event) => {
      if (!current()) return
      failed = true
      const messages = {
        'not-allowed':
          'Der Mikrofonzugriff wurde nicht erlaubt. Du kannst die Browserfreigabe ändern oder hier tippen.',
        'service-not-allowed':
          'Der Browser erlaubt diesen Sprachdienst nicht. Bitte tippe deinen Text.',
        'audio-capture': 'Es ist kein nutzbares Mikrofon verfügbar. Bitte tippe deinen Text.',
        'no-speech': 'Es wurden keine Wörter erkannt. Du kannst erneut sprechen oder tippen.',
        network:
          'Die Spracherkennung hat keine Verbindung. Dein bisheriger Text bleibt erhalten; du kannst tippen.',
        'language-not-supported':
          'Die deutsche Spracherkennung ist hier nicht verfügbar. Bitte tippe weiter.',
      }
      const message =
        messages[event.error] ||
        'Die Spracherkennung wurde unterbrochen. Dein Text bleibt erhalten; du kannst tippen.'
      abortCapture(message)
      voiceMessage(message, true)
    }
    instance.onend = () => {
      if (!current()) return
      recognition = null
      voicePhase = 'idle'
      clearVoiceTimers()
      paintVoice()
      const live = $(`#${voiceTarget}-live`)
      if (live) live.hidden = true
      if (!failed)
        voiceMessage(
          receivedWords
            ? 'Mikrofon aus. Prüfe deinen Text und sende ihn, wenn er passt.'
            : 'Mikrofon aus. Es wurden keine Wörter übernommen. Du kannst erneut sprechen oder tippen.',
        )
    }
    voiceStartTimer = window.setTimeout(() => {
      if (current()) {
        abortCapture()
        voiceMessage(
          'Der Mikrofonstart dauert zu lange. Bitte tippe weiter oder versuche es erneut.',
          true,
        )
      }
    }, 10000)
    try {
      instance.start()
    } catch {
      abortCapture()
      voiceMessage('Das Mikrofon konnte nicht gestartet werden. Du kannst weiter tippen.', true)
    }
  }
  function stopPlayback() {
    ++speechGeneration
    clearTimeout(speechTimer)
    if (speech) {
      window.speechSynthesis?.cancel()
      speech = null
    }
    document.querySelectorAll('[data-action="read"]').forEach((button) => {
      button.innerHTML = `${icon('sound')}Antwort vorlesen`
      button.setAttribute('aria-pressed', 'false')
    })
  }
  function readAloud(target) {
    if (speech) {
      stopPlayback()
      speechNotice('Vorlesen gestoppt. Die Antwort bleibt als Text sichtbar.')
      return
    }
    abortCapture()
    if (!window.speechSynthesis || !window.SpeechSynthesisUtterance)
      return speechNotice(
        'Vorlesen wird von diesem Browser nicht unterstützt. Die Antwort steht vollständig im Text.',
      )
    speechNotice('Antwort wird vorgelesen. Du kannst jederzeit stoppen.')
    const content = $(`#${target}`)?.innerText || $(`#${target}`)?.textContent
    if (!content) return
    const generation = ++speechGeneration
    const utterance = new SpeechSynthesisUtterance(content)
    utterance.lang = 'de-DE'
    utterance.rate = 0.95
    const button = $(`[data-action="read"][data-target="${target}"]`)
    speech = utterance
    button.innerHTML = `${icon('stop')}Vorlesen stoppen`
    button.setAttribute('aria-pressed', 'true')
    utterance.onend = () => {
      if (generation === speechGeneration) stopPlayback()
    }
    utterance.onerror = () => {
      if (generation === speechGeneration) {
        stopPlayback()
        speechNotice(
          'Vorlesen hat nicht funktioniert. Die Antwort bleibt vollständig als Text sichtbar.',
        )
      }
    }
    speechTimer = window.setTimeout(() => {
      if (generation === speechGeneration) {
        stopPlayback()
        speechNotice('Vorlesen beendet. Die Antwort bleibt als Text sichtbar.')
      }
    }, 90000)
    try {
      window.speechSynthesis.speak(utterance)
    } catch {
      stopPlayback()
      speechNotice('Vorlesen konnte nicht gestartet werden. Die Antwort bleibt als Text sichtbar.')
    }
  }
  function stopMedia() {
    abortCapture()
    stopPlayback()
  }
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') stopMedia()
  })
  window.addEventListener('pagehide', stopMedia)
  render(false)
})()
