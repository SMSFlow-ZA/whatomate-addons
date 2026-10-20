"""Synthetic XLS fixtures only; no employee workbooks are needed."""
import io
import unittest

import xlwt
from reader import Limits, parse


def workbook(rows):
    book = xlwt.Workbook()
    sheet = book.add_sheet("Staff")
    for r, values in enumerate(rows):
        for c, value in enumerate(values):
            sheet.write(r, c, value)
    out = io.BytesIO()
    book.save(out)
    return out.getvalue()


class ReaderTests(unittest.TestCase):
    def test_real_binary_xls(self):
        rows = [["employee_id", "phone", "active"], ["synthetic-1", "+27123456789", True]]
        self.assertEqual(parse(workbook(rows), "Staff", Limits()),
                         [["employee_id", "phone", "active"], ["synthetic-1", "+27123456789", "true"]])

    def test_size_rows_cells_and_sheet_bounds(self):
        blob = workbook([["a", "b"], ["long value", "c"]])
        for limits in [Limits(file_bytes=1), Limits(rows=1), Limits(columns=1), Limits(cell_bytes=3)]:
            with self.assertRaises(ValueError):
                parse(blob, "Staff", limits)
        with self.assertRaises(ValueError):
            parse(blob, "missing", Limits())

    def test_no_formula_evaluation(self):
        blob = workbook([[xlwt.Formula('HYPERLINK("https://example.invalid", "x")')]])
        # xlrd can read only a stored result, never recalculate or fetch URLs.
        self.assertEqual(parse(blob, "Staff", Limits()), [[""]])

    def test_rejects_other_formats_and_errors(self):
        for blob in [b"PK\\x03\\x04", b"employee,phone", b"", b"\\xd0\\xcf"]:
            with self.assertRaises(ValueError):
                parse(blob, "Staff", Limits())


if __name__ == "__main__":
    unittest.main()
