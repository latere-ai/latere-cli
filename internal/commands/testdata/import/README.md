These fixtures contain one USTAR entry, `nested/file.txt`, with contents
`fixture contents\n`, mode `0644`, and modification time zero. The compressed
files are the same tar stream encoded with gzip, bzip2, and XZ.

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
```
