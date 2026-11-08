#!/usr/bin/env python3
"""Install outside repository workspaces under the dedicated Windows identity."""
import argparse
import json
import os
import sqlite3
import subprocess
from pathlib import Path
import sys

from windows_signer import NativeTools, Signer, decode_request, load_config


def main():
    parser=argparse.ArgumentParser(description='Administrator-owned Windows MSIX signing')
    parser.add_argument('--config',required=True)
    parser.add_argument('--inspect',metavar='OPERATION_KEY')
    args=parser.parse_args()
    if sys.platform!='win32':raise ValueError('native Windows required')
    os.umask(0o077)
    config=load_config(args.config)
    # Configuration must be protected before any request or operation is accepted.
    for profile in config['profiles']:
        NativeTools(profile,Path(config['stateDir'])).privacy([
            {'path':str(Path(args.config).absolute()),'protected':False},
            {'path':str(Path(args.config).absolute().parent),'protected':True},
            {'path':config['stateDir'],'protected':True}])
    engine=Signer(config)
    result=engine.inspect(args.inspect) if args.inspect else engine.handle(decode_request(sys.stdin.buffer.read(65537)))
    print(json.dumps(result,sort_keys=True,separators=(',',':')))
    return 0


if __name__=='__main__':
    try:sys.exit(main())
    except (OSError,ValueError,KeyError,TypeError,sqlite3.Error,subprocess.TimeoutExpired):
        print('Windows signing is unconfirmed; inspect the private operation by its operationKey.',file=sys.stderr)
        sys.exit(1)
