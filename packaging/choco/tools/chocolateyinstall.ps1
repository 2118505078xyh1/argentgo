# Chocolatey install script for argentgo (Windows amd64 only).
# The checksum is the SHA-256 line for argentgo_<version>_windows_amd64.zip in
# the release's checksums.txt; release automation replaces {{SHA256}}.
# No credentials, tokens or private paths are used: the download is a public
# GitHub Releases asset.

$ErrorActionPreference = 'Stop'

$packageName = 'argentgo'
$toolsDir = Split-Path -Parent $MyInvocation.MyCommand.Definition

$url = 'https://example.com/argentgo/releases/download/v{{VERSION}}/argentgo_{{VERSION}}_windows_amd64.zip'
$checksum = '{{SHA256}}'
$checksumType = 'sha256'

$packageArgs = @{
    packageName    = $packageName
    unzipLocation  = $toolsDir
    url64bit       = $url
    checksum64     = $checksum
    checksumType64 = $checksumType
}

Install-ChocolateyZipPackage @packageArgs
