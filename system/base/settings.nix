# The fixed settings of the base system (two-stage install, CP-T1). The
# base is built once for everyone, so nothing here comes from a person's
# answers. The secure-boot modules read these keys only.
{
  secureBootEnable = true;
  secureBootShim = true;
  # The security chip unlock is enrolled in stage 2, on the installed
  # system (Q268). Until then the base opens the disk with its passphrase.
  luksTpm2Enable = false;
  luksTpm2Pin = false;
  recoveryEnable = false;
  firmwareFlashReadEntry = false;
}
