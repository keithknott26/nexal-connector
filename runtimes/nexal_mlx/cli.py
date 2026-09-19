"""Fixed local CLI; never forward arbitrary CLI flags to Python/MLX."""

import argparse
import json
import sys

from .runtime import config_from_file, infer, probe
from .ring import smoke
from .security import Rejected, verify_model


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(prog="nexal-mlx", allow_abbrev=False)
    commands = root.add_subparsers(dest="command", required=True)
    capability = commands.add_parser("probe", allow_abbrev=False)
    capability.add_argument("--load-backend", action="store_true")
    verify = commands.add_parser("verify-model", allow_abbrev=False)
    verify.add_argument("--config", required=True)
    generate = commands.add_parser("infer", allow_abbrev=False)
    generate.add_argument("--config", required=True)
    generate.add_argument("--admission", required=True)
    generate.add_argument("--prompt-file", required=True)
    generate.add_argument("--max-tokens", type=int, choices=range(1, 513), default=128,
                          metavar="1..512")
    ring = commands.add_parser("ring-smoke", allow_abbrev=False)
    ring.add_argument("--config", required=True)
    ring.add_argument("--hostfile", required=True)
    ring.add_argument("--hostfile-sha256", required=True)
    ring.add_argument("--rank", required=True, type=int, choices=range(8))
    ring.add_argument("--experimental-trusted-lan", action="store_true")
    return root


def main(argv=None) -> int:
    args = parser().parse_args(argv)
    try:
        if args.command == "probe":
            result = probe(load_backend=args.load_backend)
        elif args.command == "verify-model":
            config = config_from_file(args.config)
            manifest = verify_model(config["model_directory"], config["model_manifest_sha256"])
            result = {"verified": True, "model_id": manifest["model_id"],
                      "manifest_sha256": config["model_manifest_sha256"]}
        elif args.command == "ring-smoke":
            result = smoke(args.config, args.hostfile, args.hostfile_sha256, args.rank,
                           experimental_trusted_lan=args.experimental_trusted_lan)
        else:
            result = infer(args.config, args.admission, args.prompt_file, args.max_tokens)
        print(json.dumps(result, allow_nan=False))
        return 0
    except (Rejected, OSError, UnicodeError) as error:
        # No prompt, model contents, credential, path, or traceback on stderr.
        message = str(error) if isinstance(error, Rejected) else "local input or runtime unavailable"
        print(json.dumps({"error": {"code": "RUNTIME_REJECTED", "message": message}}), file=sys.stderr)
        return 2
    except Exception:
        print(json.dumps({"error": {"code": "RUNTIME_FAILED",
                                  "message": "approved runtime failed; inspect locally without customer data"}}),
              file=sys.stderr)
        return 3
