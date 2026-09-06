These fixtures contain one USTAR entry, `nested/file.txt`, with contents
`fixture contents\n`, mode `0644`, and modification time zero. The compressed
files are the same tar stream encoded with gzip, bzip2, and XZ.
The `payload.v7.tar` variants remove the USTAR extension fields and recompute
the first header's checksum to exercise legacy tar detection.

Regenerate with Python's standard library from this directory:

```python
import bz2, gzip, io, lzma, pathlib, tarfile
buf = io.BytesIO()
with tarfile.open(fileobj=buf, mode="w", format=tarfile.USTAR_FORMAT) as archive:
    body = b"fixture contents\n"
    entry = tarfile.TarInfo("nested/file.txt")
    entry.size, entry.mode, entry.mtime = len(body), 0o644, 0
    archive.addfile(entry, io.BytesIO(body))
raw = buf.getvalue()
for suffix, data in [("tar", raw), ("tar.gz", gzip.compress(raw, mtime=0)),
                     ("tar.bz2", bz2.compress(raw)), ("tar.xz", lzma.compress(raw))]:
    pathlib.Path("payload." + suffix).write_bytes(data)
v7 = bytearray(raw)
v7[257:512] = bytes(255)
v7[148:156] = b" " * 8
v7[148:156] = ("%06o\0 " % sum(v7[:512])).encode("ascii")
pathlib.Path("payload.v7.tar").write_bytes(v7)
pathlib.Path("payload.v7.tar.gz").write_bytes(gzip.compress(v7, mtime=0))
```
