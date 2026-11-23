package domain

// Defaults are shared by desired compilation and transactional compatibility
// checks. Saving a feature never changes the administrator's pinned version.
const (
	DefaultSingBoxVersion = "1.14.1"
	DefaultSnellVersion   = "5.0.1"
	DefaultMitaVersion    = "3.37.0"
	// Xray is not a floating administrator-supplied URL/checksum. New releases
	// need a reviewed official digest and compatibility tests before admission.
	DefaultXrayVersion = "25.12.8"
	DefaultXrayURL     = "https://github.com/XTLS/Xray-core/releases/download/v" + DefaultXrayVersion + "/Xray-linux-64.zip"
	DefaultXraySHA256  = "97f20fed49750c24fc389c2946549ba2a374907e07e9adb2ce75799dd80088d9" // gitleaks:allow public SHA-256 of the pinned official release archive
)

// Official v3.37.0 release asset digests; other pins require explicit checksums.
const DefaultMitaSHA256 = "amd64=ebd7a4f13204ac69864a385a9841708ac17e6622c1d7ef1f4415b39502c08591,arm64=3cf85a6eb70a2ad512e2d10e5c1c0a38868c8ba5291f2a43326c466a10512fe0"
