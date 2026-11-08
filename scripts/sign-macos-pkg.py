#!/usr/bin/env python3
"""Fixed private macOS signer; install outside all repository workspaces."""
import argparse
import json
import os
import sqlite3
import sys
from macos_signer import Signer, decode_request, load_config


def main():
    parser=argparse.ArgumentParser(description='Administrator-owned Developer ID PKG signing and notarization')
    parser.add_argument('--config',required=True)
    parser.add_argument('--inspect',metavar='OPERATION_KEY')
    args=parser.parse_args()
    if sys.platform!='darwin': raise ValueError('Apple signing requires a native macOS node')
    os.umask(0o077)
    engine=Signer(load_config(args.config))
    result=engine.inspect(args.inspect) if args.inspect else engine.handle(decode_request(sys.stdin.buffer.read(65537)))
    print(json.dumps(result,sort_keys=True,separators=(',',':')))
    return 0


if __name__=='__main__':
    try: sys.exit(main())
    except (OSError,ValueError,KeyError,TypeError,sqlite3.Error):
        print('macOS signing is unconfirmed; inspect the private operation by its operationKey.',file=sys.stderr)
        sys.exit(1)
