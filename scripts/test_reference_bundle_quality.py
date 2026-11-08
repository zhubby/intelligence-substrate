"""Report boundary tests; real Java cryptography is accepted separately."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import unittest
import test_sign_android_aab


class BundleQualityTests(unittest.TestCase):
    def setUp(self):
        self.fx = test_sign_android_aab.AABSigningTests('runTest')
        self.fx.setUp()
        self.addCleanup(self.fx.doCleanups)
        self.signed = Path(self.fx.fx.execute()['artifactPath'])
        self.application = self.fx.fx.root / 'application'
        self.application.mkdir()
        (self.application / 'main.txt').write_text('exact source')
        self.output = self.fx.fx.root / 'bundle-report.tar.gz'

    def run_quality(self):
        profile = self.fx.profile
        return subprocess.run([sys.executable, str(Path(__file__).with_name('verify-reference-bundle-quality.py')),
            '--application', str(self.application), '--output', str(self.output),
            '--java-home', profile['javaHome'], '--bundletool', profile['bundletoolPath'],
            '--bundletool-sha256', profile['bundletoolSHA256'], '--bundle-verifier', profile['bundleVerifierPath'],
            '--expected-package', profile['bundleId'], '--expected-signer', profile['expectedSigner']],
            env={**os.environ, 'IS_ARTIFACT_PATH': str(self.signed)}, capture_output=True, text=True)

    def report(self):
        with tarfile.open(self.output) as archive:
            self.assertEqual(archive.getnames(), ['result.json'])
            return json.load(archive.extractfile('result.json'))

    def test_bundle_report_binds_exact_signed_bytes_and_source_without_device_claim(self):
        result = self.run_quality()
        self.assertEqual(result.returncode, 0, result.stderr)
        report = self.report()
        self.assertTrue(report['passed'])
        self.assertEqual(report['artifactSHA256'], self.fx.module.sha(self.signed))
        self.assertEqual(set(report['sourceFiles']), {'main.txt'})
        self.assertEqual(report['method'], 'automated')
        self.assertEqual(report['verificationEnvironment'], 'ci')
        self.assertFalse(report['physicalDevice'])
        self.assertFalse(report['storePublished'])
        previous = self.output.read_bytes()
        self.assertNotEqual(self.run_quality().returncode, 0)
        self.assertEqual(self.output.read_bytes(), previous)

    def test_wrong_bundle_certificate_produces_failure_report(self):
        (self.fx.fx.sdk / 'jar-cert').touch()
        result = self.run_quality()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.report()['passed'])

    def test_linked_source_cannot_generate_passing_evidence(self):
        (self.application / 'secret').symlink_to(self.signed)
        self.assertNotEqual(self.run_quality().returncode, 0)
        self.assertFalse(self.report()['passed'])


if __name__ == '__main__':
    unittest.main()
