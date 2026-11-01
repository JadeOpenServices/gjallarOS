"""Refuse repo app icons (apps/<id>/icon.svg) that do more than draw.

Plane 273. Qt's SVG renderer runs no scripts, but an icon is still input
from the repo, so the build keeps it to plain shapes: no scripts, event
attributes, embedded or linked images, foreign content, external
references or entity declarations, and nothing over the size cap.
"""

import sys
import xml.etree.ElementTree as ET

MAX_BYTES = 64 * 1024
REFUSED_ELEMENTS = {"script", "foreignObject", "image", "iframe", "embed", "object", "audio", "video"}


def local(name):
    return name.rsplit("}", 1)[-1]


def problems(data):
    if len(data) > MAX_BYTES:
        yield f"larger than {MAX_BYTES} bytes"
        return
    if b"<!DOCTYPE" in data or b"<!ENTITY" in data:
        yield "has a DOCTYPE or entity declaration"
        return
    try:
        root = ET.fromstring(data)
    except ET.ParseError as error:
        yield f"not valid XML: {error}"
        return
    if local(root.tag) != "svg":
        yield "the root element is not <svg>"
    for element in root.iter():
        tag = local(element.tag)
        if tag in REFUSED_ELEMENTS:
            yield f"<{tag}> is not allowed"
        if tag == "style" and element.text and ("@import" in element.text or "url(" in element.text.replace("url(#", "")):
            yield "<style> loads something from outside"
        for key, value in element.attrib.items():
            name = local(key)
            if name.lower().startswith("on"):
                yield f"event attribute {name} on <{tag}>"
            if name == "href" and not value.startswith("#"):
                yield f"<{tag}> links outside the file: {value[:60]}"
            if "url(" in value.replace("url(#", ""):
                yield f"{name} on <{tag}> loads something from outside"


def main():
    failed = False
    for path in sys.argv[1:]:
        with open(path, "rb") as file:
            data = file.read()
        for problem in problems(data):
            print(f"{path}: {problem}", file=sys.stderr)
            failed = True
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
