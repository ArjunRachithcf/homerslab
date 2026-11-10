#!/usr/bin/python3 -I
"""Print which keys an encrypted systemd credential is bound to, from its plaintext header only.

Never decrypts. Layout and key-type IDs: systemd v259 src/shared/creds-util.{c,h}
(struct encrypted_credential_header, struct tpm2_credential_header, CRED_AES256_GCM_BY_*).
"""
import base64
import struct
import sys

KEY_TYPES = {
    "5a1c6a86df9d4096b1d5a65e0862f19a": ("host", False),
    "55b9ed1d38594d43a8319d2ebb332ac6": ("host, user-scoped", False),
    "0c7cc07b117645919c4b0bea08bc20fe": ("tpm2", True),
    "faf7eb9341e3412ca1a436f95a29362f": ("tpm2 with signed-PCR public key", True),
    "93a894094874449090caf2fc93cab553": ("host+tpm2", True),
    "ef4ac13679a9480ea7db68897f9f165d": ("host+tpm2, user-scoped", True),
    "af4950a849134eb1a73846304ff30c05": ("host+tpm2 with signed-PCR public key", True),
    "adbc4ca3efb64201ba881b6f2e4095ea": ("host+tpm2 with signed-PCR public key, user-scoped", True),
    "058469daf6f54324800549da0f8ea2fb": ("null (no key: no confidentiality)", False),
}
ALGS = {0x0001: "RSA", 0x0004: "SHA1", 0x000B: "SHA256", 0x0023: "ECC"}


def align8(n: int) -> int:
    return (n + 7) & ~7


def describe(path: str) -> str:
    with open(path, "rb") as fh:
        raw = base64.b64decode(b"".join(fh.read().split()))
    key_id = raw[:16].hex()
    name, has_tpm = KEY_TYPES.get(key_id, (f"unknown key type {key_id}", False))
    _key_size, _block_size, iv_size, _tag_size = struct.unpack_from("<IIII", raw, 16)
    out = f"{path}: {name} ({len(raw)} bytes)"
    if has_tpm:
        off = align8(32 + iv_size)
        pcr_mask, pcr_bank, primary_alg = struct.unpack_from("<QHH", raw, off)
        pcrs = [str(i) for i in range(64) if pcr_mask & (1 << i)]
        out += (f"; TPM2 PCRs: {'+'.join(pcrs) or 'none'}, bank {ALGS.get(pcr_bank, hex(pcr_bank))}, "
                f"primary key {ALGS.get(primary_alg, hex(primary_alg))}")
    return out


if __name__ == "__main__":
    for p in sys.argv[1:]:
        print(describe(p))
