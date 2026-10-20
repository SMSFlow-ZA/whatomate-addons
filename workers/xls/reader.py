"""Read-only, local legacy XLS worker. No network or formula evaluation.

Run as an isolated Linux process/container. stdout is the bounded result only;
failure messages never contain workbook values. The pure parser is unit-testable
on other platforms, but the executable refuses to run without resource limits.
"""
import argparse
from dataclasses import dataclass
import io
import json
import math
import sys


@dataclass(frozen=True)
class Limits:
    file_bytes: int = 8 * 1024 * 1024
    rows: int = 100001
    columns: int = 64
    cell_bytes: int = 4096


def parse(data, sheet_name, limits):
    import xlrd
    if (not 0 < limits.file_bytes <= 64 * 1024 * 1024 or
            not 0 < limits.rows <= 100001 or not 0 < limits.columns <= 256 or
            not 0 < limits.cell_bytes <= 65536 or not sheet_name or len(sheet_name) > 128 or
            not data or len(data) > limits.file_bytes or
            not data.startswith(bytes.fromhex("D0CF11E0A1B11AE1"))):
        raise ValueError("invalid workbook or bounds")
    try:
        book = xlrd.open_workbook(file_contents=data, on_demand=True,
                                  formatting_info=False, logfile=io.StringIO(),
                                  ignore_workbook_corruption=False)
        try:
            if book.nsheets > 64 or sheet_name not in book.sheet_names():
                raise ValueError("invalid worksheet")
            sheet = book.sheet_by_name(sheet_name)
            if sheet.nrows > limits.rows or sheet.ncols > limits.columns:
                raise ValueError("worksheet exceeds bounds")
            rows = []
            total = 0
            for r in range(sheet.nrows):
                row = []
                for c in range(sheet.ncols):
                    cell = sheet.cell(r, c)
                    if cell.ctype in (xlrd.XL_CELL_EMPTY, xlrd.XL_CELL_BLANK):
                        value = ""
                    elif cell.ctype == xlrd.XL_CELL_TEXT:
                        value = cell.value
                    elif cell.ctype == xlrd.XL_CELL_BOOLEAN:
                        value = "true" if cell.value else "false"
                    elif cell.ctype == xlrd.XL_CELL_NUMBER and math.isfinite(cell.value):
                        value = str(int(cell.value)) if cell.value.is_integer() else str(cell.value)
                    else:
                        raise ValueError("unsupported cell type")
                    size = len(value.encode("utf-8"))
                    total += size
                    if size > limits.cell_bytes or total > 32 * 1024 * 1024:
                        raise ValueError("cell data exceeds bounds")
                    row.append(value)
                rows.append(row)
            return rows
        finally:
            book.release_resources()
    except Exception as exc:
        raise ValueError("workbook rejected") from exc


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--sheet", required=True)
    parser.add_argument("--max-file", type=int, default=8 * 1024 * 1024)
    parser.add_argument("--max-rows", type=int, default=100001)
    parser.add_argument("--max-columns", type=int, default=64)
    parser.add_argument("--max-cell", type=int, default=4096)
    args = parser.parse_args()
    try:
        import resource
        resource.setrlimit(resource.RLIMIT_AS, (512 * 1024 * 1024,) * 2)
        resource.setrlimit(resource.RLIMIT_CPU, (5, 5))
        resource.setrlimit(resource.RLIMIT_FSIZE, (0, 0))
        resource.setrlimit(resource.RLIMIT_NOFILE, (32, 32))
        limits = Limits(args.max_file, args.max_rows, args.max_columns, args.max_cell)
        if not 0 < limits.file_bytes <= 64 * 1024 * 1024:
            raise ValueError("invalid bounds")
        data = sys.stdin.buffer.read(limits.file_bytes + 1)
        result = {"rows": parse(data, args.sheet, limits), "parser": "xlrd-2.0.2"}
        encoded = json.dumps(result, ensure_ascii=True, separators=(",", ":")).encode()
        if len(encoded) > 64 * 1024 * 1024:
            raise ValueError("output exceeds bound")
        sys.stdout.buffer.write(encoded)
    except Exception:
        sys.stderr.write("legacy XLS import rejected\n")
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
