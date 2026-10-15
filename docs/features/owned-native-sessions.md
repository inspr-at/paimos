# Owned session panel

The session panel has one menu: Details, Chat, Events and, when advertised at
opening, Terminal. A project's bound lead has Lead first. Existing links and
saved choices using `session`, `overview` or `messages` remain readable.
Events and Terminal never become an automatic opening choice. Live updates do
not change the opening menu or pull the reader to another pane.

The condensed header contains the session identity, the menu, Sessions list,
Full screen, More session actions and Close. The identity's tooltip includes
the complete ticket title, state, control status and registered model name.
Narrow panels and phones put layout actions in More. Phones keep More and
Close in the first row, with the menu below. In phone full screen the menu is
available from More.

Sessions list shown, list hidden and full screen apply to every pane. Layout
preferences belong to the signed-in person and are separate for phones/short
screens and larger screens. A failed save leaves a visible explanation that
the layout currently lasts for this visit. Selecting Terminal on a short
screen enters full screen without saving that automatic choice.

Arrow keys navigate the menu. Number keys 1–5 follow its visible order.
Shift+E enters or leaves full screen; F and / retain session search. Escape
leaves a field before closing, and leaves full screen before closing the
panel. These keys never override a focused holder terminal or browser/OS
modifier shortcuts. Close shows Esc in its tooltip.

All session actions live in More. Existing managed Name, Model, Effort and
Steer editors retain their ownership, catalog and receipt checks. Model names
in Details and Recent runs use the same full names as the Models registry.
Live changes above a scrolled Details reader preserve the visible content's
position, including browsers without native scroll anchoring.

Events mounts the existing bounded, privacy-filtered native event view. A
structured viewer requests only `mode: view`; it never requests a controller
capability. Replay includes the current generation, and stream attachment
uses the authenticated viewer header. Missing or disabled native endpoints
produce an unavailable/incomplete state. After a session has been identified
as owned, a failed context refresh cannot enable legacy mutation controls.

Terminal mounts the existing read-only native terminal pane or the advertised
Zellij link. Zellij opens in a new tab with `noopener noreferrer`.
This UI slice does not grant watch consent, provide a terminal parser, enable
browser input/resize, or implement the native control/runtime routes. Those
paths remain unavailable with a reason until their separately qualified
integration is supplied. Take, Hand back, End control, owned-session Chat,
settings, recovery and removal cannot claim success through a legacy route.

No provider/account qualification, release gate or live cutover is performed
by this panel change. Core wiring and Tick's visual check remain owned by the
coordinator.
