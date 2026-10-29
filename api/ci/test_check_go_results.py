import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest

from ci.check_go_results import REQUIRED_TESTS, ResultError, check_results

PKG = "github.com/vincenttanjaya/Foretrade/api/cmd/api"
OTHER = "github.com/vincenttanjaya/Foretrade/api/internal/research"
NAMES = (
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
)


def suite_events():
    # Literal Go test -json lifecycle: start, run/output/pass, package pass.
    events = [{"Action": "start", "Package": PKG}]
    for name in NAMES:
        events.extend([
            {"Action": "run", "Package": PKG, "Test": name},
            {"Action": "output", "Package": PKG, "Test": name, "Output": "--- PASS: test (0.00s)\n", "OutputType": "frame"},
            {"Action": "pass", "Package": PKG, "Test": name, "Elapsed": 0.001},
        ])
    return events + [{"Action": "pass", "Package": PKG, "Elapsed": 1.0}]


def lines(events):
    return [json.dumps(event) + "\n" for event in events]


class GoResultsTests(unittest.TestCase):
    def test_required_list_is_every_integration_test_in_the_suite(self):
        root = Path(__file__).resolve().parent.parent / "cmd" / "api"
        found = set()
        for path in root.glob("*_test.go"):
            found.update(re.findall(r"^func (TestIntegration\w+)\(t \*testing\.T\)", path.read_text(), re.M))
        self.assertEqual(found, REQUIRED_TESTS)
        self.assertEqual(set(NAMES), REQUIRED_TESTS)

    def test_complete_integration_run_and_package_without_test_files_pass(self):
        events = suite_events() + [
            {"Action": "start", "Package": OTHER},
            {"Action": "output", "Package": OTHER, "Output": f"?   \t{OTHER}\t[no test files]\n"},
            {"Action": "skip", "Package": OTHER, "Elapsed": 0.002},
        ]
        self.assertEqual(check_results(iter(lines(events))), {
            "packages_passed": 1, "tests_passed": len(NAMES),
            "packages_without_tests": 1, "integration_tests_passed": len(NAMES),
        })

    def test_parallel_subtest_lifecycle_is_complete(self):
        events = suite_events()
        events[1:1] = [
            {"Action": "run", "Package": PKG, "Test": "TestParallel"},
            {"Action": "pause", "Package": PKG, "Test": "TestParallel"},
            {"Action": "cont", "Package": PKG, "Test": "TestParallel"},
            {"Action": "run", "Package": PKG, "Test": "TestParallel/case"},
            {"Action": "pass", "Package": PKG, "Test": "TestParallel/case"},
            {"Action": "pass", "Package": PKG, "Test": "TestParallel"},
        ]
        self.assertEqual(check_results(lines(events))["tests_passed"], len(NAMES) + 2)

    def test_any_test_skip_fails_without_exposing_output(self):
        for name in (NAMES[0], "TestOptional", "TestParent/child"):
            with self.subTest(name=name):
                events = suite_events()
                events[1:1] = [
                    {"Action": "run", "Package": PKG, "Test": name},
                    {"Action": "output", "Package": PKG, "Test": name, "Output": "PRIVATE_SECRET_DSN"},
                    {"Action": "skip", "Package": PKG, "Test": name},
                ]
                with self.assertRaises(ResultError) as caught:
                    check_results(lines(events))
                self.assertIn("skip", str(caught.exception))
                self.assertNotIn("PRIVATE", str(caught.exception))

    def test_fail_actions_reject_test_package_and_build_failures(self):
        for event in (
            {"Action": "fail", "Package": PKG, "Test": NAMES[0]},
            {"Action": "fail", "Package": PKG},
            {"Action": "build-fail", "ImportPath": PKG},
        ):
            with self.subTest(event=event):
                with self.assertRaises(ResultError):
                    check_results(lines([event]) + lines(suite_events()))

    def test_package_skip_requires_no_test_files_and_no_test_events(self):
        for prior in (
            [],
            [{"Action": "output", "Package": OTHER, "Output": "custom skipped package"}],
            [{"Action": "output", "Package": OTHER, "Output": f"?   \t{OTHER}\t[no test files]\n"}, {"Action": "run", "Package": OTHER, "Test": "TestHidden"}, {"Action": "pass", "Package": OTHER, "Test": "TestHidden"}],
        ):
            with self.subTest(prior=prior):
                events = suite_events() + [{"Action": "start", "Package": OTHER}] + prior + [{"Action": "skip", "Package": OTHER}]
                with self.assertRaises(ResultError):
                    check_results(lines(events))

    def test_integration_package_cannot_claim_no_test_files(self):
        with self.assertRaises(ResultError):
            check_results(lines([
                {"Action": "start", "Package": PKG},
                {"Action": "output", "Package": PKG, "Output": f"?   \t{PKG}\t[no test files]\n"},
                {"Action": "skip", "Package": PKG},
            ]))

    def test_each_required_integration_test_and_package_pass_are_required(self):
        for missing in NAMES:
            with self.subTest(missing=missing):
                with self.assertRaises(ResultError):
                    check_results(lines([e for e in suite_events() if e.get("Test") != missing]))
        with self.assertRaises(ResultError):
            check_results(lines(suite_events()[:-1]))

    def test_empty_malformed_or_invalid_schema_rejects(self):
        for fixture in ([], ["\n"], ["not JSON\n"], ["[]\n"], ["null\n"], ["{}\n"], ['{"Action":"start","Package":null}\n']):
            with self.subTest(fixture=fixture):
                with self.assertRaises(ResultError):
                    check_results(fixture)

    def test_started_tests_and_packages_require_terminal_results(self):
        for extra in (
            [{"Action": "start", "Package": OTHER}],
            [{"Action": "start", "Package": OTHER}, {"Action": "run", "Package": OTHER, "Test": "TestUnfinished"}],
            [{"Action": "start", "Package": OTHER}, {"Action": "run", "Package": OTHER, "Test": "TestUnfinished"}, {"Action": "pass", "Package": OTHER}],
        ):
            with self.subTest(extra=extra):
                with self.assertRaises(ResultError):
                    check_results(lines(suite_events() + extra))

    def test_unstarted_and_duplicate_results_do_not_prove_success(self):
        cases = [suite_events()[1:], suite_events() + suite_events(), suite_events()[:-1] + [{"Action": "pass", "Package": PKG, "Test": NAMES[0]}] + suite_events()[-1:]]
        for events in cases:
            with self.subTest(events=events[:2]):
                with self.assertRaises(ResultError):
                    check_results(lines(events))

    def test_cli_validates_file_and_reports_safe_summary(self):
        script = Path(__file__).with_name("check_go_results.py")
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "go-tests.jsonl"
            path.write_text("".join(lines(suite_events())))
            good = subprocess.run([sys.executable, str(script), str(path)], capture_output=True, text=True)
            self.assertEqual(good.returncode, 0, good.stderr)
            self.assertIn(f"{len(NAMES)} required tests passed", good.stdout)
            path.write_text("PRIVATE_DSN\n")
            bad = subprocess.run([sys.executable, str(script), str(path)], capture_output=True, text=True)
            self.assertNotEqual(bad.returncode, 0)
            self.assertNotIn("PRIVATE", bad.stdout + bad.stderr)
            missing = subprocess.run([sys.executable, str(script), str(path.with_name("missing"))], capture_output=True, text=True)
            self.assertNotEqual(missing.returncode, 0)

    def test_cli_stdin_requires_explicit_dash(self):
        script = Path(__file__).with_name("check_go_results.py")
        good = subprocess.run([sys.executable, str(script), "-"], input="".join(lines(suite_events())), capture_output=True, text=True)
        self.assertEqual(good.returncode, 0, good.stderr)
        empty = subprocess.run([sys.executable, str(script), "-"], input="", capture_output=True, text=True)
        self.assertNotEqual(empty.returncode, 0)
        absent = subprocess.run([sys.executable, str(script)], capture_output=True, text=True)
        self.assertNotEqual(absent.returncode, 0)


if __name__ == "__main__":
    unittest.main()
