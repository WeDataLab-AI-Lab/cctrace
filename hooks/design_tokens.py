"""Serve the dashboard's design tokens to the docs site.

DESIGN.md keeps token values in exactly one place, web/app/globals.css, because
every copy of a value it ever made drifted from the original. The docs site
therefore holds no colour of its own: this hook lifts the custom properties out
of the first `:root { ... }` block of globals.css and writes them verbatim to
assets/tokens.css at build time. assets/cctrace.css refers to them by name only.

The config file sits at the repository root in the mirror and under mirror/ in
the source tree, so globals.css is looked up relative to both. Finding neither is
a build error, not a fallback: a site built without tokens renders colourless and
looks almost right.
"""

import os
import re

TOKENS_CSS = "assets/tokens.css"
_PROPERTY = re.compile(r"^\s*(--[A-Za-z0-9-]+)\s*:\s*([^;]+);", re.MULTILINE)


def _globals_css(config_dir):
    for base in (config_dir, os.path.dirname(config_dir)):
        path = os.path.join(base, "web", "app", "globals.css")
        if os.path.isfile(path):
            return path
    raise FileNotFoundError(f"web/app/globals.css not found next to or above {config_dir}")


def _root_block(css):
    start = css.index(":root {")
    end = css.index("\n}", start)
    return re.sub(r"/\*.*?\*/", "", css[start:end], flags=re.DOTALL)


def on_config(config):
    # The i18n plugin re-runs on_config once per language on the same config.
    if TOKENS_CSS not in config["extra_css"]:
        config["extra_css"].insert(0, TOKENS_CSS)
    return config


def on_post_build(config):
    path = _globals_css(os.path.dirname(config["config_file_path"]))
    with open(path, encoding="utf-8") as f:
        props = _PROPERTY.findall(_root_block(f.read()))
    if not props:
        raise ValueError(f"no custom properties in the :root block of {path}")
    out = os.path.join(config["site_dir"], TOKENS_CSS)
    os.makedirs(os.path.dirname(out), exist_ok=True)
    with open(out, "w", encoding="utf-8") as f:
        f.write("/* Generated from web/app/globals.css by hooks/design_tokens.py. Do not edit. */\n:root {\n")
        f.writelines(f"  {name}: {value.strip()};\n" for name, value in props)
        f.write("}\n")
