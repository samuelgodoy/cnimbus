# secureboot-uki

Signs the shipped EFI-stub kernel with a pure-Go Authenticode
implementation (no Docker, no external tool -- see AD-043), and
optionally assembles a signed Unified Kernel Image (kernel + initramfs
+ cmdline merged into one PE binary), so a hypervisor with Secure Boot
enabled will only run it once your own certificate is enrolled in its
UEFI database.

## Build

```bash
GOOS=linux GOARCH=amd64 go build -o helloserver ../../cmd/helloserver

# Sign the kernel only:
cnimbus build-disk -f Nimbusfile --pieces ../../pieces --secureboot

# Or assemble + sign a full UKI (implies --secureboot):
cnimbus build-disk -f Nimbusfile --pieces ../../pieces --uki
```

With no `--secureboot-key`/`--secureboot-cert`, a signing keypair is
generated once under `./secureboot/` and reused on every later build
(never silently regenerated) -- printed on the first run:

```
secureboot: no --secureboot-key/--secureboot-cert given -- generated a
new RSA-3072 signing identity under ./secureboot (reused on every later
build; enroll ./secureboot/secureboot-cert.pem into your hypervisor's
UEFI db -- see "cnimbus keygen --secureboot --out-dir" to pre-generate
one explicitly instead)
```

To bring your own certificate instead:

```bash
cnimbus build-disk -f Nimbusfile --pieces ../../pieces --uki \
  --secureboot-key mykey.pem --secureboot-cert mycert.pem
```

## Enrolling the certificate and booting with Secure Boot on

**VirtualBox** (the certificate must go into the real `db` UEFI
variable -- `enrollpk`/`enrollmok` write to the Platform Key/MokList
instead and won't satisfy boot-loader verification):

```bash
VBoxManage createvm --name secureboot-demo --ostype Linux_64 --register
VBoxManage modifyvm secureboot-demo --firmware efi64 --chipset ich9
VBoxManage modifynvram secureboot-demo inituefivarstore
VBoxManage modifynvram secureboot-demo enrollpk \
  --platform-key=./secureboot/secureboot-cert.pem \
  --owner-uuid=$(uuidgen)   # --owner-uuid is mandatory, not optional

# Build a real EFI_SIGNATURE_LIST from your cert and write it into db:
docker run --rm -v "$(pwd)/secureboot:/work" debian:trixie-slim bash -c \
  "apt-get update -qq && apt-get install -y -qq efitools && \
   cert-to-efi-sig-list -g $(uuidgen) /work/secureboot-cert.pem /work/db.esl"
VBoxManage modifynvram secureboot-demo changevar --name=db --filename=./secureboot/db.esl
VBoxManage modifynvram secureboot-demo secureboot --enable

VBoxManage convertfromraw cnimbus-secureboot-demo.img cnimbus-secureboot-demo.vmdk --format VMDK
VBoxManage storagectl secureboot-demo --name SATA --add sata
VBoxManage storageattach secureboot-demo --storagectl SATA --port 0 --device 0 \
  --type hdd --medium cnimbus-secureboot-demo.vmdk
VBoxManage startvm secureboot-demo --type headless
```

Without the certificate enrolled into `db`, the same VM fails to boot
with a real UEFI firmware message: `BdsDxe: failed to load Boot0001
"UEFI VBOX HARDDISK ..." ... Access Denied` -- that's Secure Boot
actually doing its job, and a good way to confirm the mechanism is live
before troubleshooting anything else.

**Hyper-V**: as of this writing, `Set-VMFirmware -SecureBootTemplate`
only accepts Microsoft's own built-in templates
(`MicrosoftWindows`/`MicrosoftUEFICertificateAuthority`/
`OpenSourceShieldedVM`) -- there is no cmdlet to enroll a custom
certificate, so this walkthrough doesn't have a Hyper-V equivalent yet.
