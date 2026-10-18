#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Builds arion_w2.json.gz, the fixture of TestDeliveryNumbersEqualMeasureV2 (AEON-1016).

Input: measure-v2-records.json, the value-free records Project Arion v5's
measure-v2.py wrote (ids, SHAs, timestamps, conclusions, job names; no titles,
bodies or logs). Output keeps only what the Delivery numbers read, with branch
names and SHAs replaced by stable stand-ins (the numbers depend on identity,
never on the name). Push runs are dropped: no Delivery number reads them.

    python3 arion_w2_convert.py measure-v2-records.json arion_w2.json.gz
"""
import gzip, hashlib, json, re, sys

src, dst = sys.argv[1], sys.argv[2]
d = json.load(open(src))
required = d["required_checks"]
sha = lambda s: hashlib.sha1(("aeon-1016:" + s).encode()).hexdigest()
branches = {}
def branch(name):
    return branches.setdefault(name, "work/b%03d" % (len(branches) + 1))
runs = []
for r in d["runs"]:
    if r["event"] not in ("pull_request", "merge_group"):
        continue
    name = r["head_branch"]
    m = re.match(r"^(gh-readonly-queue/main/pr-\d+-)([0-9a-f]{40})$", name or "")
    name = m.group(1) + sha(m.group(2)) if m else branch(name)
    wait = r["worst_job_wait_min"]
    runs.append([r["id"], r["event"], name, sha(r["head_sha"]), r["created_at"], r["a1_conclusion"], r["a1_started"], r["a1_completed"],
                 r["latest_attempt"], r["latest_conclusion"], r["latest_completed"], [r["required"][n] for n in required], wait])
out = {"provenance": "Project Arion v5 measure-v2 records, windows W1/W2 of 2026-10-05..09; names and SHAs anonymized",
       "windows": d["windows"], "required": required, "runs": runs,
       "pulls": [[p["number"], p["created_at"], p["merged_at"]] for p in d["merged_prs"]]}
with gzip.GzipFile(dst, "wb", mtime=0) as f:
    f.write(json.dumps(out, separators=(",", ":")).encode())
print(len(runs), "runs", len(out["pulls"]), "pulls")
