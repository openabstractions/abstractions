"""Native unit checks for the chat@3 managed helper and shared typed corpus."""
from pathlib import Path
import unittest
import abstraction.inference.api as api
from abstraction.inference.client import ChatV3, FoldV3, ObserveRefused, InconsistentReply

CORPUS = Path(__file__).with_name("chat3_managed.jsonl")


class ManagedChat3(unittest.TestCase):
    def test_policy_local_only_codec(self):
        request = api.decode_request_v3(CORPUS.with_name("chat3_policy_local_only.json").read_bytes())
        roundtrip = api.decode_request_v3(api.encode_request_v3(request))
        for r in (request, roundtrip):
            self.assertEqual(r.target.selection_policy, "private")
            self.assertEqual(r.target.model, "")
            self.assertEqual(r.content.guarantees, [api.ChatGuarantee.LOCAL_ONLY])
            self.assertEqual(r.content.credential, "")
            self.assertEqual((r.identity.key, r.identity.history_epoch), ("privacy-key", "epoch"))

    def test_fold(self):
        events = [api.decode_chat_event(line) for line in CORPUS.read_bytes().splitlines()]
        suffix = FoldV3(False, 2)
        for event in events[2:]:
            suffix.add(event)
        self.assertEqual((len(suffix.reply()["blocks"]), len(suffix.reply()["block_ends"])), (4, 5))
        for outcome in (api.ChatResultOutcome.COMPLETED, api.ChatResultOutcome.FAILED, api.ChatResultOutcome.CANCELLED):
            import copy
            f = FoldV3()
            part, end = copy.deepcopy(events[1]), copy.deepcopy(events[-1])
            part.sequence, end.sequence = 0, 1
            end.end.outcome = outcome
            if outcome != api.ChatResultOutcome.COMPLETED:
                end.end.finish_reason = None
            f.add(part)
            f.add(end)
            self.assertEqual(f.reply()["result"].outcome, outcome)
            self.assertEqual(f.reply()["block_ends"], [])
        for retain in (False, True):
            fold = FoldV3(retain)
            for event in events:
                self.assertIsNone(fold.reply())
                fold.add(event)
            r = fold.reply()
            self.assertEqual(r["text"], "Hello world")
            self.assertEqual(len(r["blocks"]), 5)
            self.assertEqual(r["blocks"][3].arguments, "{}")
            self.assertEqual(r["blocks"][4].text, "declined")
            self.assertEqual(bool(r["blocks"][0].text), retain)
            self.assertEqual(bool(r["blocks"][1].text), retain)
            self.assertEqual(r["blocks"][0].seal.origin, "vendor/opaque")
            self.assertEqual(r["blocks"][0].seal.bytes, bytes([0, 255, 65]))
            self.assertEqual(r["block_ends"][1].seal_omitted, api.SealOmission.ENCODED_SIZE)
            self.assertEqual(fold.progress.done, 0)
            self.assertIsNone(fold.progress.total)
            self.assertEqual(r["result"].usage.input, 0)
            self.assertIsNone(r["result"].usage.reasoning)
            self.assertEqual(r["result"].served_by.server, "fixture")
            timing = r["result"].timing
            self.assertEqual((timing.queue_ms, timing.load_ms, timing.prefill_ms, timing.decode_ms, timing.reused_tokens), (0, 31, 14, 20, 0))
            self.assertIsNone(timing.restore_ms)
            self.assertEqual((timing.load, timing.load_source), ("future_load", "future_source"))
            self.assertEqual(r["result"].finish_reason, api.FinishReason.OTHER)
            self.assertEqual(events[-1].end.finish_reason, "future_vendor_finish")
            with self.assertRaises(InconsistentReply):
                fold.add(events[-1])
        fold = FoldV3()
        with self.assertRaises(InconsistentReply):
            fold.add(events[1])
        for event in events[:3]:
            fold.add(event)
        events[1].sequence = 3
        with self.assertRaises(InconsistentReply):
            fold.add(events[1])

    def test_resume_never_resubmits_or_cancels_wait_errors(self):
        class Port(ChatV3):
            def __init__(self):
                self.starts, self.cancels, self.calls = 0, 0, []
                self.error = TimeoutError("wait deadline")
            def start(self, request):
                self.starts += 1
                return api.ChatAdmission(outcome=api.ChatStartOutcome.UNKNOWN)
            def observe(self, request, cursor):
                self.calls.append((request, cursor))
                if self.error:
                    raise self.error
                return api.ChatPage(outcome=api.PageOutcome.GAP, next=9)
            def cancel(self, request):
                self.cancels += 1
        port = Port()
        unknown = port.open(None)
        self.assertEqual(unknown.admission.outcome, api.ChatStartOutcome.UNKNOWN)
        self.assertEqual(port.starts, 1)
        session = port.resume("retained", 7)
        for error in (TimeoutError("deadline"), ConnectionError("disconnect")):
            port.error = error
            with self.assertRaises(type(error)):
                session.next()
            self.assertEqual((session.request, session.cursor, session.fold.cursor), ("retained", 7, 7))
        port.error = None
        with self.assertRaises(ObserveRefused):
            session.next()
        self.assertEqual((port.starts, port.cancels, session.cursor), (1, 0, 7))
        session.cancel()
        self.assertEqual(port.cancels, 1)


if __name__ == "__main__":
    unittest.main()
