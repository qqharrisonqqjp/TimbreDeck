"""No torch dependency. Check the observer does not change decode semantics."""
from pathlib import Path
import importlib.util
from types import SimpleNamespace
import unittest

spec = importlib.util.spec_from_file_location("td_worker", Path(__file__).resolve().parents[1] / "studio" / "worker.py")
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)


class ObserveDecode(unittest.TestCase):
    def test_retains_identity_and_device(self):
        for value, wanted in ((SimpleNamespace(device="cuda:0"), {"cuda:0"}),
                              ([SimpleNamespace(device="cpu"), SimpleNamespace(device="cuda:0")], {"cpu","cuda:0"}),
                              (None, {"unknown"})):
            result = SimpleNamespace(audio_values=value)
            model = SimpleNamespace(decode=lambda x, *, flag: result if (x,flag)==(1,True) else None)
            devices=set()
            worker.observe_tokenizer_decode(model,devices)
            self.assertIs(model.decode(1,flag=True),result)
            self.assertEqual(devices,wanted)

    def test_missing_and_failed_decode(self):
        devices=set()
        worker.observe_tokenizer_decode(SimpleNamespace(),devices)
        def err():
            raise ValueError("decode failed")
        model=SimpleNamespace(decode=err)
        worker.observe_tokenizer_decode(model,devices)
        with self.assertRaisesRegex(ValueError,"decode failed"):
            model.decode()
        self.assertFalse(devices)

if __name__=="__main__":
    unittest.main()
