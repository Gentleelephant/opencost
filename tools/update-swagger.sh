#!/usr/bin/env sh
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

cd "$ROOT_DIR"

swag init \
  -g main.go \
  -d cmd/costmodel,pkg/costmodel,pkg/cloudcost,pkg/customcost,pkg/cloud/config \
  --output docs \
  --outputTypes json

npx -y swagger2openapi docs/swagger.json -o docs/swagger.json

node <<'NODE'
const fs = require("fs");

const file = "docs/swagger.json";
const routePrefix = "/kapis/costwise.wiztelemetry.io/v1alpha1";
const doc = JSON.parse(fs.readFileSync(file, "utf8"));

if (doc.paths && typeof doc.paths === "object") {
  doc.paths = Object.fromEntries(
    Object.entries(doc.paths).filter(([path]) => path.startsWith(routePrefix)),
  );
}

fs.writeFileSync(file, JSON.stringify(doc, null, 2) + "\n");
NODE
