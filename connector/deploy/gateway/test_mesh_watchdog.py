import importlib.util, os, unittest

spec = importlib.util.spec_from_file_location("wd", os.path.join(os.path.dirname(__file__), "nexal-mesh-watchdog.py"))
wd = importlib.util.module_from_spec(spec)
spec.loader.exec_module(wd)


def doc(*peers):
    return {"peers": {"details": list(peers)}}


def peer(rx, tx, status="Connected", reason="", profile="nexal-mlkem1024-tcp-v2", key="k1"):
    return {"publicKey": key, "fqdn": "mac-mini-m4.peers.mesh.nexal.systems", "status": status,
            "transferReceived": rx, "transferSent": tx, "quantumReason": reason, "quantumProfile": profile}


class Decide(unittest.TestCase):
    def run_samples(self, samples, step=30):
        state, t, out = {}, 1000.0, None
        for s in samples:
            out = wd.decide(doc(s), state, t)
            t += step
        return out

    def test_black_hole(self):
        got = self.run_samples([peer(1000 + 32 * i, 5000 + 500 * i) for i in range(6)])
        self.assertIsNotNone(got)
        self.assertIn("black hole", got[1])

    def test_answering_peer_is_fine(self):
        self.assertIsNone(self.run_samples([peer(1000 + 900 * i, 5000 + 500 * i) for i in range(6)]))

    def test_idle_peer_is_fine(self):
        self.assertIsNone(self.run_samples([peer(1000 + 32 * i, 5000 + 32 * i) for i in range(10)]))

    def test_needs_a_full_window(self):
        self.assertIsNone(self.run_samples([peer(1000, 5000 + 2000 * i) for i in range(4)]))  # 90 s only

    def test_counter_reset_starts_over(self):
        samples = [peer(1000, 5000 + 500 * i) for i in range(3)] + [peer(10, 10 + 500 * i) for i in range(3)]
        self.assertIsNone(self.run_samples(samples))

    def test_stuck_pending(self):
        got = self.run_samples([peer(100, 100, reason="session-pending") for _ in range(12)])
        self.assertIsNotNone(got)
        self.assertIn("session-pending", got[1])

    def test_brief_pending_during_renewal_is_fine(self):
        samples = [peer(100, 100, reason="session-pending" if i % 4 == 0 else "") for i in range(30)]
        self.assertIsNone(self.run_samples(samples))

    def test_disconnected_peer_ignored(self):
        self.assertIsNone(self.run_samples([peer(0, 5000 + 900 * i, status="Connecting") for i in range(8)]))


if __name__ == "__main__":
    unittest.main()
