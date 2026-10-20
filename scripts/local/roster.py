"""Create local-only fictional import files; this does not approve the roster."""
import argparse, csv, json
from pathlib import Path
from openpyxl import Workbook

p = argparse.ArgumentParser()
p.add_argument("runtime", type=Path)
a = p.parse_args()
if a.runtime.name != "synthetic-local" or a.runtime.parent.name != ".work":
    raise SystemExit("Expected private .work/synthetic-local directory")
identities = json.loads((a.runtime / "identities.json").read_text())
if identities.get("Synthetic") is not True:
    raise SystemExit("Synthetic identities required")
rows = []
for i, user in enumerate(identities["Logins"]):
    rows.append([user["EmployeeID"], f"+120255501{i:02d}", "true", "demo-er", "Synthetic ER"])
for i in range(8, 36):
    rows.append([f"DEMO-{i:03d}", f"+120255501{i:02d}", "true", "demo-er", "Synthetic Operations"])
header = ["Employee ID", "Phone", "Active", "Zone", "Department"]
with (a.runtime / "SYNTHETIC-roster.csv").open("w", newline="", encoding="utf-8") as f:
    w = csv.writer(f); w.writerow(header); w.writerows(rows)
book = Workbook(); sheet = book.active; sheet.title = "Synthetic roster"
sheet.append(header)
for row in rows: sheet.append(row)
book.save(a.runtime / "SYNTHETIC-roster.xlsx")
print("Created 36 fictional reserved-number rows. Local simulator only; never dial.")
