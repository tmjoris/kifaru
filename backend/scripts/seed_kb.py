#!/usr/bin/env python3
"""Seeds the knowledge base known_good suppression list into a running platform."""
import json, argparse
from pathlib import Path
import httpx

DATA = Path(__file__).parent.parent / "data"

ap = argparse.ArgumentParser()
ap.add_argument("--url", default="http://127.0.0.1:8000")
a = ap.parse_args()

kb = json.loads((DATA / "kifaru_knowledge_base.json").read_text())
labels = kb.get("known_good_labels", {})
c = httpx.Client(base_url=a.url, timeout=20)

n = 0
for h in kb.get("known_good", []):
    r = c.post("/v1/admin/kb", json={"artefact_hash": h, "list_name": "known_good",
                                     "label": labels.get(h, "legitimate high fan-in"),
                                     "added_by": "seed"})
    n += r.status_code == 200

print(f"  seeded {n} known_good artefacts")
print(f"  NOTE: known_bad is left EMPTY - the agent populates it itself as it validates.")
