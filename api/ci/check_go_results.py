"""Require complete Go JSON test results with no skipped integration tests.

The integration suite skips without INTEGRATION_DATABASE_URL, so a green
`go test` alone does not prove it ran. This fails unless every started package
and test finished, none failed or skipped, and every required integration test
passed. Add a new integration test to REQUIRED_TESTS.
"""

import argparse
from contextlib import nullcontext
import json
import re
import sys

PACKAGE = "github.com/vincenttanjaya/Foretrade/api/cmd/api"
REQUIRED_TESTS = frozenset((
    "TestIntegrationAuth",
    "TestIntegrationRoutesRequireSession",
    "TestIntegrationTrading",
    "TestIntegrationStrategies",
    "TestIntegrationAIPortfolio",
    "TestIntegrationCopilot",
    "TestIntegrationForecast",
    "TestIntegrationChat",
    "TestIntegrationNews",
    "TestIntegrationArena",
    "TestIntegrationCredits",
    "TestIntegrationAgent",
    "TestIntegrationForecastRecord",
    "TestIntegrationAutopilot",
    "TestIntegrationSignalsAndPerformance",
    "TestIntegrationChatActions",
    "TestIntegrationTradeDesk",
    "TestIntegrationForecastMarket",
    "TestIntegrationQuality",
    "TestIntegrationForecastMarketThinAndEvents",
    "TestIntegrationForecastUpdate",
    "TestIntegrationAlerts",
    "TestIntegrationDesk",
    "TestIntegrationAdminSeed",
    "TestIntegrationPortfolioDrivers",
    "TestIntegrationWorkflows",
    "TestIntegrationChatResilience",
    "TestIntegrationForecastResearchFallback",
    "TestIntegrationForecastProgress",
    "TestIntegrationMarketPositions",
    "TestIntegrationChatQuickOrder",
    "TestIntegrationChatMarketOdds",
    "TestIntegrationOddsAlerts",
    "TestIntegrationStrategyCommunity",
    "TestIntegrationForecastLadder",
    "TestIntegrationLiveArena",
    "TestIntegrationPredictionBoardDirectOdds",
    "TestIntegrationUniverse",
    "TestIntegrationChatImages",
    "TestIntegrationChatAttachments",
    "TestIntegrationAccountReads",
    "TestIntegrationScreener",
    "TestIntegrationProfile",
    "TestIntegrationResearch",
    "TestIntegrationLifecycleDrain",
    "TestIntegrationQuoteFeed",
    "TestIntegrationDrawdownAlerts",
    "TestIntegrationForecastDeepResearch",
    "TestIntegrationForecastShrinkGuard",
    "TestIntegrationForecastReferenceNoteKept",
    "TestIntegrationForecastMaterials",
    "TestIntegrationChatOutlookTSM",
    "TestIntegrationForecastMethods",
    "TestIntegrationForecastMethodsYahooDown",
    "TestIntegrationListed",
    "TestIntegrationScreenerLive",
    "TestIntegrationRealtimeQuotes",
    "TestIntegrationRealtimeVenues",
    "TestIntegrationListeners",
))


class ResultError(ValueError):
    """The test result stream does not prove a successful complete run."""


def _object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON field")
        result[key] = value
    return result


def _invalid_constant(value):
    raise ValueError("invalid JSON number")


def _name(value):
    if not isinstance(value, str) or not value or len(value) > 512 or any(ord(c) < 32 for c in value):
        raise ResultError("invalid package or test name")
    return value


def check_results(lines):
    """Return counts only after every started package/test finishes successfully."""
    packages = {}
    count = 0
    for count, line in enumerate(lines, 1):
        if count > 200_000 or not isinstance(line, str) or len(line) > 2 << 20:
            raise ResultError("result stream exceeds limits")
        try:
            event = json.loads(line, object_pairs_hook=_object, parse_constant=_invalid_constant)
        except (ValueError, RecursionError):
            raise ResultError(f"malformed JSON at line {count}") from None
        if not isinstance(event, dict) or not isinstance(event.get("Action"), str):
            raise ResultError(f"invalid event at line {count}")
        action = event["Action"]
        package = _name(event.get("ImportPath") if action.startswith("build-") else event.get("Package"))
        test = _name(event["Test"]) if "Test" in event else None
        if action in ("fail", "build-fail"):
            raise ResultError(f"failure: {package}" + (f"/{test}" if test else ""))
        if action == "skip" and test:
            raise ResultError(f"test skipped: {package}/{test}")
        if action == "build-output":
            if test or not isinstance(event.get("Output"), str):
                raise ResultError("invalid build output event")
            continue
        if action == "start" and not test:
            if package in packages:
                raise ResultError(f"duplicate package start: {package}")
            packages[package] = {"status": "running", "tests": {}, "no_files": False}
            continue
        state = packages.get(package)
        if state is None or state["status"] != "running":
            raise ResultError(f"event outside an active package: {package}")
        tests = state["tests"]
        if action == "output":
            output = event.get("Output")
            if not isinstance(output, str) or (test and tests.get(test) not in ("running", "paused")):
                raise ResultError(f"invalid output event: {package}")
            if not test and re.fullmatch(r"\?\s+" + re.escape(package) + r"\s+\[no test files\]\s*", output):
                state["no_files"] = True
        elif test:
            previous = tests.get(test)
            if action == "run" and previous is None:
                tests[test] = "running"
            elif action == "pause" and previous == "running":
                tests[test] = "paused"
            elif action == "cont" and previous == "paused":
                tests[test] = "running"
            elif action == "pass" and previous == "running":
                tests[test] = "pass"
            else:
                raise ResultError(f"invalid or duplicate test lifecycle: {package}/{test}")
        elif action == "pass":
            if any(status != "pass" for status in tests.values()):
                raise ResultError(f"unfinished tests: {package}")
            state["status"] = "pass"
        elif action == "skip":
            if package == PACKAGE or tests or not state["no_files"]:
                raise ResultError(f"package skipped without proving no test files: {package}")
            state["status"] = "no_files"
        else:
            raise ResultError(f"unknown event action at line {count}")
    if count == 0:
        raise ResultError("empty result stream")
    for package, state in packages.items():
        if state["status"] == "running":
            raise ResultError(f"unfinished package: {package}")
    suite = packages.get(PACKAGE)
    if suite is None or suite["status"] != "pass":
        raise ResultError("required integration package did not pass")
    missing = REQUIRED_TESTS - suite["tests"].keys()
    if missing:
        raise ResultError(f"missing {len(missing)} required integration tests: {sorted(missing)[0]}")
    return {
        "packages_passed": sum(s["status"] == "pass" for s in packages.values()),
        "tests_passed": sum(len(s["tests"]) for s in packages.values()),
        "packages_without_tests": sum(s["status"] == "no_files" for s in packages.values()),
        "integration_tests_passed": len(REQUIRED_TESTS),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("results", help="Go test JSONL file, or - for standard input")
    args = parser.parse_args()
    try:
        source = nullcontext(sys.stdin) if args.results == "-" else open(args.results, encoding="utf-8")
        with source as lines:
            stats = check_results(lines)
    except (ResultError, OSError, UnicodeError) as error:
        reason = str(error) if isinstance(error, ResultError) else "result file could not be read"
        print(f"Go checks failed: {reason}", file=sys.stderr)
        return 1
    print(f"Go checks passed: {stats['packages_passed']} packages, {stats['tests_passed']} tests; "
          f"{stats['packages_without_tests']} packages had no test files. "
          f"Integration: {stats['integration_tests_passed']} required tests passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
