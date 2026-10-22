# Start agent: project lead queue

The expert Start agent dialog checks the selected ticket’s project before
enabling manual run setup. A configured project shows an early notice and a
Queue for lead action. The queue uses the existing project permission and
readiness checks; no host, harness, account, model or thinking choice is needed.
Projects without a configured lead retain the manual setup flow.

Queueing records demand for the project lead. It does not confirm an agent
start. When automatic launch is unavailable or the lead is paused, the dialog
explains that queued work waits. Failed settings reads keep launch disabled
and offer a retry; failed queue writes remain visible in the dialog.

The notice follows the selected ticket and dialog opening. Late responses are
discarded after selection or identity changes. Desktop actions sit above
content that grows downward; phone sheets pin their action bar to the bottom.
