// SPDX-License-Identifier: AGPL-3.0-only
// Browser counterpart of the coordinator's remote-test.sh. Ship only committed
// HEAD, use the approved mba@mbp2606 lane, preserve artifacts, never fall back.
import { spawn, spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import { resolve, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('../', import.meta.url))
const host = 'mba@mbp2606.local'
const sshArgs = ['-o', 'BatchMode=yes', '-o', 'ConnectTimeout=8', host]
export const quote = value => `'${String(value).replaceAll("'", "'\\''")}'`

// Check again under the host-wide browser lock, before extracting or installing
// dependencies. PID/comm inspection is unnecessary: only known lane PID files.
export const remoteProbe = `
set -eu
export PATH="$HOME/.nix-profile/bin:/nix/var/nix/profiles/default/bin:$PATH"
[ ! -e "$HOME/.aeon-builder-on" ] || { echo 'remote refused: builder pool active' >&2; exit 3; }
console_user=$(stat -f %Su /dev/console)
idle=$(ioreg -c IOHIDSystem | awk '/HIDIdleTime/ {print int($NF/1000000000); exit}')
load=$(sysctl -n vm.loadavg | awk '{print $2}')
case "$console_user" in ''|mailina) echo 'remote refused: console presence' >&2; exit 3;; esac
case "$idle" in ''|*[!0-9]*) echo 'remote refused: idle unknown' >&2; exit 3;; esac
[ "$console_user" = ci ] || [ "$idle" -ge 600 ] || { echo 'remote refused: keyboard active' >&2; exit 3; }
awk -v load="$load" 'BEGIN { exit !(load ~ /^[0-9]+([.][0-9]+)?$/ && load <= 18) }' || { echo 'remote refused: load' >&2; exit 3; }
for marker in "$HOME"/.aeon-remote-test/*.pid; do
  [ -e "$marker" ] || continue
  echo 'remote refused: another heavy run reserved capacity' >&2; exit 3
done
command -v node >/dev/null && command -v npm >/dev/null && command -v git >/dev/null || { echo 'remote prerequisites missing' >&2; exit 3; }
`

export function remoteScript(run, args) {
  if (!/^[a-f0-9]{12}-[a-f0-9-]{36}$/.test(run)) throw new Error('Invalid remote run identity')
  return `${remoteProbe}
mkdir -p "$HOME/aeon-ui-runs" "$HOME/.aeon-remote-test"
lock="$HOME/.aeon-ui-remote.lock"
mkdir "$lock" 2>/dev/null || { echo 'remote refused: browser lane already reserved' >&2; exit 3; }
marker="$HOME/.aeon-remote-test/ui-${run}.pid"
suite_pid=''
finish() {
  # Only these exact, self-created reservation files are removed. Run files and
  # artifacts remain for inspection; no worktree or unrelated state is pruned.
  if [ -n "$suite_pid" ] && node --input-type=module -e 'import { existsSync } from "node:fs"; import { suiteLockPath } from "./scripts/playwright-global-setup.mjs"; process.exit(existsSync(suiteLockPath) ? 0 : 1)' 2>/dev/null; then
    echo 'remote cleanup not proven; reservations retained for inspection' >&2
    return
  fi
  [ ! -e "$marker" ] || unlink "$marker"
  [ ! -e "$lock/pid" ] || unlink "$lock/pid"
  rmdir "$lock"
}
trap finish EXIT
stop_suite() {
  if [ -n "$suite_pid" ]; then kill -TERM "$suite_pid" 2>/dev/null || true; wait "$suite_pid" || true; fi
}
trap 'stop_suite; exit 130' INT
trap 'stop_suite; exit 143' TERM HUP
# Close the race with another remote browser run. Go's existing lane also sees
# this reservation and refuses new work while it exists.
${remoteProbe}
echo $$ > "$marker"
echo $$ > "$lock/pid"
run_dir="$HOME/aeon-ui-runs/${run}"
mkdir "$run_dir"
tar -xf - -C "$run_dir"
cd "$run_dir/web"
npm ci --no-audit --no-fund
# OPS owns the one-time pinned browser install. Never install it implicitly.
node --input-type=module -e 'import { accessSync, constants } from "node:fs"; import registryModule from "./node_modules/playwright-core/lib/server/registry/index.js"; try { accessSync(registryModule.registry.findExecutable("chromium-headless-shell").executablePath(), constants.X_OK) } catch { console.error("remote refused: pinned headless shell missing; OPS-247 owns installation"); process.exit(3) }'
set +e
CI= PW_WORKERS=1 node ../scripts/playwright-safe.mjs -c playwright.ui.config.ts ${args.map(quote).join(' ')} --workers=1 &
suite_pid=$!
echo "$suite_pid" > "$run_dir/supervisor.pid"
wait "$suite_pid"
rc=$?
unlink "$run_dir/supervisor.pid"
cd "$run_dir"
exit "$rc"
`
}

function call(command, args, options = {}) {
  const result = spawnSync(command, args, { cwd: root, encoding: 'utf8', ...options })
  if (result.error) throw result.error
  return result
}

export async function runRemote(args) {
  // The lane's hold/capture controls are owned by the coordinator. Requiring
  // its directory avoids silently ignoring a reservation outside this repo.
  const controls = process.env.AEON_REMOTE_CONTROL_DIR
  if (!controls || !existsSync(controls)) throw new Error('Set AEON_REMOTE_CONTROL_DIR to the coordinator directory containing remote-test.sh and its OPS hold controls. Use draft PR CI while the lane is unavailable.')
  if (existsSync(join(controls, '.hold-mbp2606'))) throw new Error('Remote lane held by OPS; use draft PR CI. No local fallback.')
  if (existsSync(join(controls, '.capture-open'))) throw new Error('Remote capture window reserved; use draft PR CI. No local fallback.')
  // An optional OPS-owned launcher wraps the entire SSH job once OPS-247 is
  // available. Its executable accepts: browser -- <command> <args...>.
  // It is opt-in: no invented autodetection or edits to workstation tooling.
  if (process.env.AEON_HEAVY_JOB_LANE && !process.env.AEON_UI_LANE_ACTIVE) {
    const lane = call(process.env.AEON_HEAVY_JOB_LANE, ['browser', '--', process.execPath, fileURLToPath(import.meta.url), ...args], {
      stdio: 'inherit', env: { ...process.env, AEON_UI_LANE_ACTIVE: '1' },
    })
    return lane.status ?? 1
  }
  const status = call('git', ['--no-optional-locks', 'status', '--porcelain', '--untracked-files=no'])
  if (status.status !== 0 || status.stdout.trim()) throw new Error('Commit tracked changes first: the remote lane tests committed HEAD only')
  const revision = call('git', ['rev-parse', 'HEAD'])
  if (revision.status !== 0) throw new Error('Cannot resolve committed HEAD')
  const sha = revision.stdout.trim()
  const probe = call('ssh', [...sshArgs, 'bash -s'], { input: remoteProbe })
  if (probe.status !== 0) { console.error('Remote presence/capacity check refused or unreachable; use draft PR CI. No local fallback.'); return 3 }
  const run = `${sha.slice(0, 12)}-${randomUUID()}`
  const artifacts = resolve(root, 'web/test-results/remote', run)
  mkdirSync(artifacts, { recursive: true })
  console.log(`Remote UI run ${run}; committed HEAD ${sha}; artifacts ${artifacts}`)
  const remote = spawn('ssh', [...sshArgs, `bash -c ${quote(remoteScript(run, args))}`], { cwd: root, stdio: ['pipe', 'pipe', 'pipe'] })
  // git archive streams HEAD without pushing any additional branch or ref.
  const archive = spawn('git', ['archive', sha], { cwd: root, stdio: ['ignore', 'pipe', 'inherit'] })
  archive.stdout.pipe(remote.stdin)
  remote.stdin.on('error', () => { /* refusal closes the archive pipe */ })
  const archiveExit = new Promise(resolveExit => {
    archive.once('error', () => resolveExit(1))
    archive.once('exit', code => resolveExit(code ?? 1))
  })
  let log = ''
  for (const stream of [remote.stdout, remote.stderr]) stream.on('data', data => { process.stdout.write(data); log += data.toString() })
  const signalHandlers = new Map()
  let interrupted = false
  for (const signal of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
    const handler = () => {
      if (interrupted) return
      interrupted = true
      // Signal the exact run's supervisor before closing the SSH transport.
      // Its process group cleanup completes before the remote reservation ends.
      const stop = spawn('ssh', [...sshArgs, `bash -c ${quote(`file="$HOME/aeon-ui-runs/${run}/supervisor.pid"; if [ -f "$file" ]; then read -r pid < "$file"; case "$pid" in ''|*[!0-9]*) exit 3;; esac; kill -TERM "$pid"; fi` )}`], { stdio: 'inherit' })
      stop.once('error', () => { console.error('Could not signal the remote run; its reservation is retained for inspection') })
      archive.kill(signal)
    }
    signalHandlers.set(signal, handler)
    process.on(signal, handler)
  }
  let code
  try {
    code = await new Promise((resolveExit, reject) => { remote.once('error', reject); remote.once('close', value => resolveExit(value ?? 1)) })
    if (await archiveExit) code ||= 1
    if (interrupted) code = 130
  } finally { for (const [signal, handler] of signalHandlers) process.off(signal, handler) }
  writeFileSync(join(artifacts, 'run.log'), log)
  // Fetch only this run's test output. A failed transfer never becomes success.
  const copied = call('scp', ['-q', '-r', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=8', `${host}:aeon-ui-runs/${run}/web/test-results`, join(artifacts, 'results')], { stdio: 'inherit' })
  writeFileSync(join(artifacts, 'run.json'), `${JSON.stringify({ run, commit: sha, exit_code: code, artifacts_copied: copied.status === 0 })}\n`)
  if (code === 0 && copied.status !== 0) code = 1
  return code
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = await runRemote(process.argv.slice(2)) }
  catch (error) { console.error(error.message); process.exitCode = 3 }
}
