# Remove an unstarted lead

A person with `harness.control` in the project can choose **Remove** from the
lead menu on Agents → Leads or the project lead card. Choosing Remove first
shows an inline confirmation; choosing it again removes the lead. Cancel keeps
the lead. Queued work stays queued, and the project card shows **No lead**.

Only a lead that never acquired a session or generation can be removed. A lead
that has run uses the existing pause, yield and succession controls, even after
its process stops. Removal requires the displayed revision, rechecks permission
under the same project fence as start/claim, and writes `lead.removed` with the
previous lead snapshot in the same transaction. Generation history remains
immutable. A later start uses a revision newer than the removed intent, so an
old confirmation cannot remove its replacement. No launch authority, scopes or
credentials are added.
