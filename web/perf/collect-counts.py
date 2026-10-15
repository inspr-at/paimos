# SPDX-License-Identifier: AGPL-3.0-only
"""Count-only PPM inventory. Raw API content stays in memory and is never logged."""
import collections
import concurrent.futures
import datetime
import json
import subprocess
import sys
import urllib.parse

CLI = "/Users/markus/.local/share/aeon-cli/aeon"


def call(*args):
    result = subprocess.run([CLI, "--instance", "ppm", "--json", *args],
                            capture_output=True, timeout=45)
    if result.returncode:
        raise RuntimeError("PPM count read failed; response withheld")
    if len(result.stdout) > 16 * 1024 * 1024:
        raise RuntimeError("PPM response exceeded inventory bound")
    return json.loads(result.stdout)


def pages(path, params, cap=500):
    cursor = None
    for _ in range(cap):
        query = dict(params)
        if cursor:
            query["cursor"] = cursor
        result = call("curl", path + "?" + urllib.parse.urlencode(query))
        yield result["items"]
        following = result.get("next_cursor")
        if not following:
            return
        if following == cursor:
            raise RuntimeError("PPM pagination did not advance")
        cursor = following
    raise RuntimeError("PPM inventory exceeded page bound")


def details(node):
    comments = sum(item["type"] == "comment"
                   for page in pages("/api/nodes/" + node + "/activity", {"limit": 200})
                   for item in page)
    # IDs are used only to deduplicate edges in memory; they never leave this process.
    edges = {item["id"] for page in pages("/api/relations", {"node_id": node, "limit": 200})
             for item in page}
    return comments, edges


def inventory(key):
    project = call("project", "show", key)["id"]
    ids, shapes = [], collections.Counter()
    for page in pages("/api/nodes", {"within": project, "kind": "work", "limit": 500}):
        for row in page:
            ids.append(row["id"])
            shapes[(row["depth"], row["work_children_count"], bool(row["is_leaf"]))] += 1
    total_comments, relation_ids, comment_histogram = 0, set(), collections.Counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        for index, (comments, edges) in enumerate(pool.map(details, ids), 1):
            total_comments += comments
            comment_histogram[comments] += 1
            relation_ids.update(edges)
            if index % 100 == 0:
                print(f"{key}: counted {index}/{len(ids)} work items", file=sys.stderr)
    return {"key": key, "work_items": len(ids), "visible_comments": total_comments,
            "incident_relations": len(relation_ids),
            "comment_histogram": {str(k): v for k, v in sorted(comment_histogram.items())},
            "shapes": [{"depth": d, "children": c, "leaf": leaf, "count": count}
                       for (d, c, leaf), count in sorted(shapes.items())]}


if __name__ == "__main__":
    projects = [inventory(key) for key in ("AIT", "AEON")]
    print(json.dumps({"captured_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                      "source": "ppm; active kind=work; visible comments and incident relations",
                      "projects": projects}, indent=2))
