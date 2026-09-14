// Inline icons — a handful of 16px glyphs, kept here so the bundle carries no
// icon font or sprite sheet.
const svg = (path, props = {}) => (p) => (
  <svg
    viewBox="0 0 16 16"
    width={p.size || 16}
    height={p.size || 16}
    fill={props.fill || "currentColor"}
    aria-hidden="true"
    className={p.className}
    {...(props.stroke ? { stroke: "currentColor", fill: "none", strokeWidth: 1.5, strokeLinecap: "round", strokeLinejoin: "round" } : {})}
  >
    {path}
  </svg>
);

export const IconChevron = svg(<path d="M6 12l4-4-4-4" />, { stroke: true });
export const IconChevronDown = svg(<path d="M4 6l4 4 4-4" />, { stroke: true });
export const IconBack = svg(<path d="M13 8H3.5M7 3.5L3 8l4 4.5" />, { stroke: true });
export const IconForward = svg(<path d="M3 8h9.5M9 3.5L13 8l-4 4.5" />, { stroke: true });
export const IconFile = svg(<path d="M3 1.5h6L13 5.5v9H3z M9 1.5V6h4" />, { stroke: true });
export const IconComment = svg(<path d="M2 3.5h12v8H8l-3.5 3v-3H2z" />, { stroke: true });
export const IconSearch = svg(<><circle cx="7" cy="7" r="4.5" /><path d="M10.5 10.5L14 14" /></>, { stroke: true });
export const IconSymbol = svg(<path d="M5.5 2.5L2 8l3.5 5.5M10.5 2.5L14 8l-3.5 5.5" />, { stroke: true });
export const IconCheck = svg(<path d="M3 8.5l3.5 3.5L13 4.5" />, { stroke: true });
export const IconRefresh = svg(<path d="M13.5 8a5.5 5.5 0 1 1-1.7-4M13.5 2v3.5H10" />, { stroke: true });
export const IconX = svg(<path d="M4 4l8 8M12 4l-8 8" />, { stroke: true });
export const IconPlus = svg(<path d="M8 3.5v9M3.5 8h9" />, { stroke: true });
export const IconExpand = svg(<path d="M5 6.5L8 3.5l3 3M5 9.5l3 3 3-3" />, { stroke: true });
export const IconCollapse = svg(<path d="M5 3.5l3 3 3-3M5 12.5l3-3 3 3" />, { stroke: true });
export const IconFilter = svg(<path d="M2.5 3h11L9.2 8.3V13l-2.4-1.2V8.3z" />, { stroke: true });
export const IconSplit = svg(<><rect x="2" y="3" width="12" height="10" rx="1" /><path d="M8 3v10" /></>, { stroke: true });
export const IconUnified = svg(<><rect x="2" y="3" width="12" height="10" rx="1" /><path d="M2 8h12" /></>, { stroke: true });
export const IconBranch = svg(<><circle cx="4" cy="3.5" r="1.8" /><circle cx="4" cy="12.5" r="1.8" /><circle cx="12" cy="6" r="1.8" /><path d="M4 5.3v5.4M12 7.8c0 2-1.5 2.9-4 3.2" /></>, { stroke: true });
export const IconDots = svg(<><circle cx="3.5" cy="8" r="1.2" /><circle cx="8" cy="8" r="1.2" /><circle cx="12.5" cy="8" r="1.2" /></>);
export const IconMoon = svg(<path d="M13 9.5A5.5 5.5 0 0 1 6.5 3a5.5 5.5 0 1 0 6.5 6.5z" />, { stroke: true });
export const IconSun = svg(<><circle cx="8" cy="8" r="3" /><path d="M8 1v1.5M8 13.5V15M1 8h1.5M13.5 8H15M3 3l1 1M12 12l1 1M13 3l-1 1M4 12l-1 1" /></>, { stroke: true });
export const IconSpark = svg(<path d="M8 1.5l1.5 4L13.5 7 9.5 8.5 8 12.5 6.5 8.5 2.5 7l4-1.5zM12.5 11l.7 1.8 1.8.7-1.8.7-.7 1.8-.7-1.8-1.8-.7 1.8-.7z" />);
export const IconKeyboard = svg(<><rect x="1.5" y="4" width="13" height="8" rx="1.5" /><path d="M4.5 7h.01M7 7h.01M9.5 7h.01M12 7h.01M5 9.5h6" /></>, { stroke: true });
export const IconWrap = svg(<path d="M3 4h9a2 2 0 0 1 0 4H6M8 6l-2 2 2 2M3 12h6" />, { stroke: true });
export const IconPin = svg(<path d="M6 2h4M7 2v4.5L4.5 9h7L9 6.5V2M8 9v5" />, { stroke: true });
export const IconUndo = svg(<path d="M2.5 8a5.5 5.5 0 1 0 1.7-4M2.5 2v3.5H6" />, { stroke: true });
export const IconBell = svg(<path d="M4 11.5V7a4 4 0 0 1 8 0v4.5l1.2 1.2H2.8zM6.5 14.2a1.6 1.6 0 0 0 3 0" />, { stroke: true });
