# Administrator-installed helper. Only fixed certificate/ACL inspection is exposed.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false)
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$OutputEncoding = [Console]::OutputEncoding

function Get-Digest($Bytes, $Algorithm) {
    $hash = [System.Security.Cryptography.HashAlgorithm]::Create($Algorithm)
    try { return ([BitConverter]::ToString($hash.ComputeHash($Bytes))).Replace('-', '') }
    finally { $hash.Dispose() }
}

function Test-Chain($Certificate, $Purpose) {
    $chain = New-Object System.Security.Cryptography.X509Certificates.X509Chain
    try {
        $chain.ChainPolicy.RevocationMode = [System.Security.Cryptography.X509Certificates.X509RevocationMode]::Online
        $chain.ChainPolicy.RevocationFlag = [System.Security.Cryptography.X509Certificates.X509RevocationFlag]::ExcludeRoot
        $chain.ChainPolicy.UrlRetrievalTimeout = [TimeSpan]::FromSeconds(15)
        $null = $chain.ChainPolicy.ApplicationPolicy.Add((New-Object System.Security.Cryptography.Oid($Purpose)))
        return $chain.Build($Certificate)
    } finally { $chain.Dispose() }
}

function Get-CertificateInfo($Certificate) {
    $purpose = @($Certificate.EnhancedKeyUsageList | ForEach-Object { $_.ObjectId.Value })
    $now = [DateTime]::UtcNow
    return @{
        certificateSHA256 = (Get-Digest $Certificate.RawData 'SHA256').ToLowerInvariant()
        certificateSHA1 = (Get-Digest $Certificate.RawData 'SHA1')
        publisher = $Certificate.Subject
        codeSigning = ($purpose -contains '1.3.6.1.5.5.7.3.3')
        chainValid = (Test-Chain $Certificate '1.3.6.1.5.5.7.3.3')
        validTime = ($Certificate.NotBefore.ToUniversalTime() -le $now -and $Certificate.NotAfter.ToUniversalTime() -gt $now)
    }
}

try {
    $body = [Console]::In.ReadToEnd()
    if ($body.Length -gt 65536) { throw 'Request limit' }
    $request = $body | ConvertFrom-Json
    if ($request.action -eq 'privacy') {
        $current = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
        $allowed = @($current, 'S-1-5-18', 'S-1-5-32-544')
        if (@($request.paths).Count -gt 20) { throw 'Path limit' }
        foreach ($entry in $request.paths) {
            if (-not [System.IO.Path]::IsPathRooted($entry.path)) { throw 'Absolute path required' }
            $item = Get-Item -LiteralPath $entry.path -Force
            if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Reparse point refused' }
            $acl = Get-Acl -LiteralPath $entry.path
            $owner = (New-Object System.Security.Principal.NTAccount($acl.Owner)).Translate([System.Security.Principal.SecurityIdentifier]).Value
            if ($allowed -notcontains $owner -or ($entry.protected -and -not $acl.AreAccessRulesProtected)) { throw 'Private owner and protected root required' }
            foreach ($rule in $acl.Access) {
                $sid = $rule.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value
                if ($rule.AccessControlType -eq [System.Security.AccessControl.AccessControlType]::Allow -and $allowed -notcontains $sid) { throw 'Private signing ACL required' }
            }
        }
        $result = @{ private = $true }
    } elseif ($request.action -eq 'identity') {
        if ($request.storeLocation -notin @('CurrentUser', 'LocalMachine') -or $request.certificateSHA1 -notmatch '^[A-F0-9]{40}$') { throw 'Fixed store identity required' }
        $path = 'Cert:\' + $request.storeLocation + '\My\' + $request.certificateSHA1
        $cert = Get-Item -LiteralPath $path
        $result = Get-CertificateInfo $cert
        $result.hasPrivateKey = [bool]$cert.HasPrivateKey
        if ($cert.PublicKey.Oid.Value -ne '1.2.840.113549.1.1.1') { throw 'RSA code signing identity required' }
    } elseif ($request.action -eq 'verify') {
        $signature = Get-AuthenticodeSignature -LiteralPath $request.path
        if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid -or $null -eq $signature.SignerCertificate) { throw 'Valid Authenticode signature required' }
        $result = Get-CertificateInfo $signature.SignerCertificate
        $result.status = $signature.Status.ToString()
        $result.timestamped = ($null -ne $signature.TimeStamperCertificate)
        $result.timestampChainValid = ($result.timestamped -and (Test-Chain $signature.TimeStamperCertificate '1.3.6.1.5.5.7.3.8'))
    } else { throw 'Unsupported inspection action' }
    $result | ConvertTo-Json -Compress -Depth 6
} catch {
    [Console]::Error.WriteLine('Native Windows signing inspection is unconfirmed; inspect private diagnostics.')
    exit 1
}
