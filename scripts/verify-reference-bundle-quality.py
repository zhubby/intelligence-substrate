#!/usr/bin/env python3
"""Independently verify the exact Google Play upload bundle; CI evidence only."""
import argparse
from datetime import datetime, timezone
import importlib.util
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import tarfile
import tempfile
import android_bundle as bundle


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['application', 'output', 'java-home', 'bundletool', 'bundle-verifier']:
        parser.add_argument('--' + name, type=Path, required=True)
    for name in ['bundletool-sha256', 'expected-package', 'expected-signer']:
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args()
    if (not re.fullmatch(r'[a-f0-9]{64}', args.expected_signer)
            or not re.fullmatch(r'[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+', args.expected_package)
            or not os.environ.get('IS_ARTIFACT_PATH')):
        parser.error('exact artifact, package and upload certificate required')
    output = args.output.absolute()
    if output.exists() or output.is_symlink():
        parser.error('report already exists')
    profile = {'javaHome': str(args.java_home), 'bundletoolPath': str(args.bundletool),
               'bundleVerifierPath': str(args.bundle_verifier), 'bundletoolSHA256': args.bundletool_sha256,
               'bundleId': args.expected_package}
    report = {'schemaVersion': 1, 'kind': 'exact-upload-bundle-validation', 'method': 'automated',
              'verificationEnvironment': 'ci', 'physicalDevice': False, 'emulator': False,
              'storePublished': False, 'enterpriseIdentityVerified': False,
              'startedAt': datetime.now(timezone.utc).isoformat()}
    with tempfile.TemporaryDirectory(prefix='is-bundle-quality-') as temporary:
        directory = Path(temporary)
        try:
            original = Path(os.environ['IS_ARTIFACT_PATH']).absolute()
            bundle.archive(original, signed=True)
            staged = directory / 'exact.aab'
            shutil.copyfile(original, staged)
            digest = bundle.sha(staged)
            metadata = bundle.inspect(profile, directory, staged, args.expected_signer)
            payload = bundle.archive(staged, signed=True)
            if bundle.sha(original) != digest:
                raise ValueError('artifact changed during independent verification')
            spec = importlib.util.spec_from_file_location('android_quality', Path(__file__).with_name('verify-reference-android-quality.py'))
            quality = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(quality)
            application = args.application.absolute()
            if application.is_symlink() or not application.is_dir():
                raise ValueError('regular source directory required')
            files = quality.source_snapshot(application)
            report.update(passed=True, artifactSHA256=digest, artifactBytes=staged.stat().st_size,
                certificateSHA256=args.expected_signer, bundleMetadata=metadata, payload=payload,
                bundletoolSHA256=args.bundletool_sha256, sourceFiles=files,
                sourceDigest=hashlib.sha256(json.dumps(files, sort_keys=True, separators=(',', ':')).encode()).hexdigest())
        except Exception as error:
            report.update(passed=False, error=str(error))
        report['finishedAt'] = datetime.now(timezone.utc).isoformat()
        result = directory / 'result.json'
        result.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
        output.parent.mkdir(parents=True, exist_ok=True)
        fd, name = tempfile.mkstemp(prefix='.bundle-report-', dir=output.parent)
        os.close(fd)
        try:
            with tarfile.open(name, 'w:gz') as archive:
                archive.add(result, arcname='result.json', recursive=False)
            os.link(name, output)
        finally:
            os.unlink(name)
    print(json.dumps({'passed': report['passed'], 'report': str(output),
                      'artifactSHA256': report.get('artifactSHA256'), 'error': report.get('error')}))
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
