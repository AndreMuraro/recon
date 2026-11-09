"""Runs a recon conformance script on a local Spark and fails unless it passes.

Usage: python run-spark-sql.py SCRIPT [SESSION_TIME_ZONE] [ANSI]

The script is one statement that returns a single row "pass", or one row per mismatch
(see internal/dialect/scripts_test.go). The session time zone defaults to a non-UTC one,
so that a dialect relying on UTC sessions is caught.
"""

import sys

from pyspark.sql import SparkSession


def main() -> int:
    path = sys.argv[1]
    time_zone = sys.argv[2] if len(sys.argv) > 2 else "America/Sao_Paulo"
    ansi = sys.argv[3] if len(sys.argv) > 3 else "true"
    spark = (
        SparkSession.builder.master("local[2]")
        .appName("recon-conformance")
        .config("spark.ui.enabled", "false")
        .config("spark.sql.session.timeZone", time_zone)
        .config("spark.sql.ansi.enabled", ansi)
        .getOrCreate()
    )
    spark.sparkContext.setLogLevel("ERROR")
    with open(path, encoding="utf-8") as f:
        statement = f.read().rstrip().rstrip(";")
    rows = spark.sql(statement).collect()
    print(f"Spark {spark.version}, session time zone {time_zone}, ANSI {ansi}")
    for row in rows[:50]:
        print(*row, sep="\t")
    if len(rows) == 1 and rows[0]["result"] == "pass":
        return 0
    print(f"{len(rows)} mismatching values", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
