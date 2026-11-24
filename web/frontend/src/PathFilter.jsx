// PathFilterPanel is the repository's file filters, as the diff's file list
// and a board ticket's changes both show them: generated files, and the
// patterns of files to include and exclude.
export function PathFilterPanel({ hideGenerated, onHideGenerated, generatedCount, pathFilter, onPathFilter }) {
  return (
    <div className="path-filter">
      <label className="check">
        <input type="checkbox" className="tick" checked={hideGenerated} onChange={(e) => onHideGenerated(e.target.checked)} />
        Hide generated files
        <span className="count">{generatedCount}</span>
      </label>
      {[
        ["include", "files to include", "src/, *.go"],
        ["exclude", "files to exclude", "*_test.go, docs/"],
      ].map(([key, label, hint]) => (
        <label key={key}>
          {label}
          <input
            value={pathFilter[key]}
            placeholder={hint}
            onChange={(e) => {
              const v = e.target.value;
              onPathFilter((f) => ({ ...f, [key]: v }));
            }}
            onKeyDown={(e) => e.stopPropagation()}
          />
        </label>
      ))}
    </div>
  );
}

// The filters set: nothing included or excluded.
export const NO_FILTER = { include: "", exclude: "" };
