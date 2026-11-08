// Administrator-owned all-entry JAR signature verifier. No network or key access.
import java.io.InputStream;
import java.nio.file.Path;
import java.security.CodeSigner;
import java.security.MessageDigest;
import java.security.cert.X509Certificate;
import java.util.HexFormat;
import java.util.Locale;
import java.util.HashSet;
import java.util.jar.JarFile;
import java.util.regex.Pattern;

class VerifyAndroidBundle {
    static boolean metadata(String name) {
        return Pattern.matches("(?i)META-INF/(MANIFEST\\.MF|[^/]+\\.(SF|RSA|DSA|EC))", name);
    }

    public static void main(String[] args) throws Exception {
        if (args.length != 2 || !(args[1].equals("unsigned") || args[1].matches("[a-f0-9]{64}"))) {
            throw new IllegalArgumentException("exact bundle path and certificate/unsigned state required");
        }
        boolean signed = !args[1].equals("unsigned");
        int count = 0;
        HashSet<String> signatures = new HashSet<>();
        try (JarFile jar = new JarFile(Path.of(args[0]).toFile(), true)) {
            var entries = jar.entries();
            byte[] buffer = new byte[65536];
            while (entries.hasMoreElements()) {
                var entry = entries.nextElement();
                if (entry.isDirectory()) continue;
                // Consume every byte: JarFile authenticates signatures during reads.
                try (InputStream input = jar.getInputStream(entry)) {
                    while (input.read(buffer) != -1) {}
                }
                if (metadata(entry.getName())) {
                    if (!entry.getName().equalsIgnoreCase("META-INF/MANIFEST.MF")) {
                        signatures.add(entry.getName().toUpperCase(Locale.ROOT));
                    }
                    continue;
                }
                count++;
                CodeSigner[] signers = entry.getCodeSigners();
                if (!signed) {
                    if (signers != null) throw new SecurityException("unsigned bundle contains signed content");
                    continue;
                }
                if (signers == null || signers.length != 1) {
                    throw new SecurityException("every AAB payload file needs one upload signer");
                }
                X509Certificate certificate = (X509Certificate) signers[0].getSignerCertPath().getCertificates().get(0);
                certificate.checkValidity();
                String digest = HexFormat.of().formatHex(MessageDigest.getInstance("SHA-256").digest(certificate.getEncoded()));
                if (!digest.equals(args[1]) || certificate.getSubjectX500Principal().getName().toLowerCase(Locale.ROOT).contains("cn=android debug")) {
                    throw new SecurityException("AAB upload certificate differs from approved release identity");
                }
            }
        }
        if (count == 0 || (!signed && !signatures.isEmpty()) || (signed && (signatures.size() != 2
                || signatures.stream().filter(n -> n.endsWith(".SF")).count() != 1
                || signatures.stream().filter(n -> n.matches(".*\\.(RSA|DSA|EC)")).count() != 1))) {
            throw new SecurityException("unexpected or missing JAR signature metadata");
        }
        System.out.println("{\"signed\":" + signed + ",\"certificateSHA256\":\"" + (signed ? args[1] : "")
                + "\",\"payloadEntries\":" + count + "}");
    }
}
