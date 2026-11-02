package config

// ManagementIntent says whether a JODS server manages the device and how
// it reaches it (JODS-565). With Enable off the device is a standalone
// install and the other fields are not used.
type ManagementIntent struct {
	Enable                          bool   `json:"enable"`
	Endpoint                        string `json:"endpoint"`
	PolicySigningPublicKey          string `json:"policySigningPublicKey"`
	RecoveryCommandSigningPublicKey string `json:"recoveryCommandSigningPublicKey"`
	EnrollmentMode                  string `json:"enrollmentMode"`
	AllowInsecureTLS                bool   `json:"allowInsecureTls"`
	DeviceClass                     string `json:"deviceClass"`
	DesktopProfile                  string `json:"desktopProfile"`
	FingerprintEnrollmentAllowed    bool   `json:"fingerprintEnrollmentAllowed"`
}

// PrebootLockIntent turns on the JODS preboot lock.
type PrebootLockIntent struct {
	Enable bool `json:"enable"`
}
