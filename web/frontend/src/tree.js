// The sidebar's file tree, shaped like VS Code's explorer: folders before
// files, names in natural order ("file2" before "file10"), and a chain of
// folders that each hold nothing but one folder shown as a single row.

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

// compareTreePaths orders paths the way the tree lists them. The diff pane uses
// it too, so stepping between files and scrolling walk the sidebar top to bottom.
export function compareTreePaths(a, b) {
  return compareParts(a.split("/"), b.split("/"));
}

// sortTreePaths orders a whole repository's paths, splitting each one once
// rather than on every comparison.
export function sortTreePaths(paths) {
  return paths
    .map((p) => [p.split("/"), p])
    .sort(([x], [y]) => compareParts(x, y))
    .map(([, p]) => p);
}

function compareParts(x, y) {
  for (let i = 0; i < x.length && i < y.length; i++) {
    const xDir = i < x.length - 1;
    const yDir = i < y.length - 1;
    if (xDir !== yDir) return xDir ? -1 : 1;
    // The collator folds case, so "Foo" and "foo" need a tie-break to stay two rows.
    const c = collator.compare(x[i], y[i]) || (x[i] < y[i] ? -1 : x[i] > y[i] ? 1 : 0);
    if (c) return c;
  }
  return 0;
}

// buildTree nests files, already in compareTreePaths order, under their
// folders; that order is what lets children be appended as they are met. Each
// folder keeps the paths beneath it so a collapsed row can still speak for them.
// A path ending in "/" is an ignored folder not yet listed: the folder alone.
export function buildTree(files) {
  const root = { children: [] };
  const dirs = new Map();
  for (const f of files) {
    const parts = f.path.split("/");
    if (f.path.endsWith("/")) parts.pop();
    let parent = root;
    let path = "";
    for (let i = 0; i < parts.length - 1; i++) {
      path = i ? path + "/" + parts[i] : parts[i];
      let dir = dirs.get(path);
      if (!dir) {
        dir = { dir: true, name: parts[i], path, children: [], paths: [] };
        dirs.set(path, dir);
        parent.children.push(dir);
      }
      dir.paths.push(f.path);
      parent = dir;
    }
    if (!f.path.endsWith("/")) {
      parent.children.push({ name: parts[parts.length - 1], path: f.path, file: f });
      continue;
    }
    const at = parts.join("/");
    let dir = dirs.get(at);
    if (!dir) {
      dir = { dir: true, name: parts[parts.length - 1], path: at, children: [], paths: [] };
      dirs.set(at, dir);
      parent.children.push(dir);
    }
    dir.ignored = true;
  }
  compact(root);
  return root.children;
}

function compact(node) {
  node.children = node.children.map((c) => {
    while (c.dir && c.children.length === 1 && c.children[0].dir) {
      const only = c.children[0];
      c = { ...only, name: c.name + "/" + only.name };
    }
    if (c.dir) compact(c);
    return c;
  });
}

// visibleRows flattens the tree into the rows on screen, skipping what sits
// inside a folded folder.
export function visibleRows(nodes, isOpen, depth = 0, out = []) {
  for (const n of nodes) {
    out.push({ node: n, depth });
    if (n.dir && isOpen(n.path)) visibleRows(n.children, isOpen, depth + 1, out);
  }
  return out;
}

// ancestorsOf lists the folder rows a file sits under, outermost first.
export function ancestorsOf(nodes, path, out = []) {
  const dir = nodes.find((n) => n.dir && path.startsWith(n.path + "/"));
  if (!dir) return out;
  out.push(dir.path);
  return ancestorsOf(dir.children, path, out);
}

export function dirPaths(nodes, out = []) {
  for (const n of nodes) {
    if (!n.dir) continue;
    out.push(n.path);
    dirPaths(n.children, out);
  }
  return out;
}

export function ignoredDirs(nodes, out = new Set()) {
  for (const n of nodes) {
    if (!n.dir) continue;
    if (n.ignored) out.add(n.path);
    ignoredDirs(n.children, out);
  }
  return out;
}
