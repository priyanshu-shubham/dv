// Inline icons — a handful of 16px glyphs, kept here so the bundle carries no
// icon font or sprite sheet.
const svg = (path, props = {}) => (p) => (
  <svg
    viewBox={props.viewBox || "0 0 16 16"}
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
export const IconChevronUp = svg(<path d="M4 10l4-4 4 4" />, { stroke: true });
export const IconBack = svg(<path d="M13 8H3.5M7 3.5L3 8l4 4.5" />, { stroke: true });
export const IconForward = svg(<path d="M3 8h9.5M9 3.5L13 8l-4 4.5" />, { stroke: true });
export const IconFile = svg(<path d="M3 1.5h6L13 5.5v9H3z M9 1.5V6h4" />, { stroke: true });
export const IconEye = svg(<><path d="M1.5 8S4 3.5 8 3.5 14.5 8 14.5 8 12 12.5 8 12.5 1.5 8 1.5 8z" /><circle cx="8" cy="8" r="2" /></>, { stroke: true });
export const IconComment =svg(<path d="M2 3.5h12v8H8l-3.5 3v-3H2z" />, { stroke: true });
export const IconAsk = svg(<path d="M2 2.5h12v9H8.5L5 14.5v-3H2z M6.3 5.7a1.7 1.7 0 1 1 2.4 1.55c-.45.25-.7.55-.7 1.05 M8 9.9v.01" />, { stroke: true });
export const IconNewSession = svg(
  <path d="M8 2.5c3.31 0 6 2.24 6 5s-2.69 5-6 5a6.9 6.9 0 0 1-2.2-.35L2.5 13.5l.85-2.55A4.6 4.6 0 0 1 2 7.5c0-2.76 2.69-5 6-5z M8 5.5v4M6 7.5h4" />,
  { stroke: true },
);
export const IconReply = svg(<path d="M6.5 4L3 7.5 6.5 11M3 7.5h6.5A3.5 3.5 0 0 1 13 11v1.5" />, { stroke: true });
export const IconSettings = svg(<><path d="M2.5 4.5h6.5M13 4.5h.5M2.5 11.5h.5M7 11.5h6.5" /><circle cx="11" cy="4.5" r="2" /><circle cx="5" cy="11.5" r="2" /></>, { stroke: true });
export const IconTemporary =svg(<circle cx="8" cy="8" r="5.5" strokeDasharray="2.4 2" />, { stroke: true });
export const IconSearch = svg(<><circle cx="7" cy="7" r="4.5" /><path d="M10.5 10.5L14 14" /></>, { stroke: true });
export const IconSymbol = svg(<path d="M5.5 2.5L2 8l3.5 5.5M10.5 2.5L14 8l-3.5 5.5" />, { stroke: true });
export const IconCheck = svg(<path d="M3 8.5l3.5 3.5L13 4.5" />, { stroke: true });
export const IconRefresh = svg(<path d="M13.5 8a5.5 5.5 0 1 1-1.7-4M13.5 2v3.5H10" />, { stroke: true });
export const IconX = svg(<path d="M4 4l8 8M12 4l-8 8" />, { stroke: true });
export const IconPlus = svg(<path d="M8 3.5v9M3.5 8h9" />, { stroke: true });
export const IconMinus = svg(<path d="M3.5 8h9" />, { stroke: true });
export const IconExpand = svg(<path d="M5 6.5L8 3.5l3 3M5 9.5l3 3 3-3" />, { stroke: true });
export const IconCollapse = svg(<path d="M5 3.5l3 3 3-3M5 12.5l3-3 3 3" />, { stroke: true });
export const IconFilter = svg(<path d="M2.5 3h11L9.2 8.3V13l-2.4-1.2V8.3z" />, { stroke: true });
// Three paths, so each line can move on its own (.menu-glyph).
export const IconMenu = svg(<><path d="M2.5 4h11" /><path d="M2.5 8h11" /><path d="M2.5 12h11" /></>, { stroke: true });
export const IconSplit =svg(<><rect x="2" y="3" width="12" height="10" rx="1" /><path d="M8 3v10" /></>, { stroke: true });
export const IconUnified = svg(<><rect x="2" y="3" width="12" height="10" rx="1" /><path d="M2 8h12" /></>, { stroke: true });
export const IconBranch = svg(<><circle cx="4" cy="3.5" r="1.8" /><circle cx="4" cy="12.5" r="1.8" /><circle cx="12" cy="6" r="1.8" /><path d="M4 5.3v5.4M12 7.8c0 2-1.5 2.9-4 3.2" /></>, { stroke: true });
export const IconDots = svg(<><circle cx="3.5" cy="8" r="1.2" /><circle cx="8" cy="8" r="1.2" /><circle cx="12.5" cy="8" r="1.2" /></>);
export const IconMoon = svg(<path d="M13 9.5A5.5 5.5 0 0 1 6.5 3a5.5 5.5 0 1 0 6.5 6.5z" />, { stroke: true });
export const IconSun = svg(<><circle cx="8" cy="8" r="3" /><path d="M8 1v1.5M8 13.5V15M1 8h1.5M13.5 8H15M3 3l1 1M12 12l1 1M13 3l-1 1M4 12l-1 1" /></>, { stroke: true });
export const IconSpark = svg(<path d="M8 1.5l1.5 4L13.5 7 9.5 8.5 8 12.5 6.5 8.5 2.5 7l4-1.5zM12.5 11l.7 1.8 1.8.7-1.8.7-.7 1.8-.7-1.8-1.8-.7 1.8-.7z" />);
export const IconBolt = svg(<path d="M9 1.5L3.5 9H8l-1 5.5L12.5 7H8z" />, { stroke: true });
export const IconKeyboard = svg(<><rect x="1.5" y="4" width="13" height="8" rx="1.5" /><path d="M4.5 7h.01M7 7h.01M9.5 7h.01M12 7h.01M5 9.5h6" /></>, { stroke: true });
export const IconWrap = svg(<path d="M3 4h9a2 2 0 0 1 0 4H6M8 6l-2 2 2 2M3 12h6" />, { stroke: true });
export const IconPin = svg(<path d="M6 2h4M7 2v4.5L4.5 9h7L9 6.5V2M8 9v5" />, { stroke: true });
export const IconArrowUp = svg(<path d="M8 13V3.5M3.5 8L8 3.5 12.5 8" />, { stroke: true });
export const IconStop = svg(<rect x="4" y="4" width="8" height="8" rx="1.5" />);
export const IconUndo = svg(<path d="M2.5 8a5.5 5.5 0 1 0 1.7-4M2.5 2v3.5H6" />, { stroke: true });
export const IconEdit = svg(<path d="M10.5 3l2.5 2.5L6 12.5H3.5V10zM9 4.5L11.5 7" />, { stroke: true });
export const IconBell = svg(<path d="M4 11.5V7a4 4 0 0 1 8 0v4.5l1.2 1.2H2.8zM6.5 14.2a1.6 1.6 0 0 0 3 0" />, { stroke: true });

// The agents' own marks, from simple-icons (CC0): Claude's, and OpenAI's for Codex.
export const IconClaude = svg(
  <path d="m4.7144 15.9555 4.7174-2.6471.079-.2307-.079-.1275h-.2307l-.7893-.0486-2.6956-.0729-2.3375-.0971-2.2646-.1214-.5707-.1215-.5343-.7042.0546-.3522.4797-.3218.686.0608 1.5179.1032 2.2767.1578 1.6514.0972 2.4468.255h.3886l.0546-.1579-.1336-.0971-.1032-.0972L6.973 9.8356l-2.55-1.6879-1.3356-.9714-.7225-.4918-.3643-.4614-.1578-1.0078.6557-.7225.8803.0607.2246.0607.8925.686 1.9064 1.4754 2.4893 1.8336.3643.3035.1457-.1032.0182-.0728-.164-.2733-1.3539-2.4467-1.445-2.4893-.6435-1.032-.17-.6194c-.0607-.255-.1032-.4674-.1032-.7285L6.287.1335 6.6997 0l.9957.1336.419.3642.6192 1.4147 1.0018 2.2282 1.5543 3.0296.4553.8985.2429.8318.091.255h.1579v-.1457l.1275-1.706.2368-2.0947.2307-2.6957.0789-.7589.3764-.9107.7468-.4918.5828.2793.4797.686-.0668.4433-.2853 1.8517-.5586 2.9021-.3643 1.9429h.2125l.2429-.2429.9835-1.3053 1.6514-2.0643.7286-.8196.85-.9046.5464-.4311h1.0321l.759 1.1293-.34 1.1657-1.0625 1.3478-.8804 1.1414-1.2628 1.7-.7893 1.36.0729.1093.1882-.0183 2.8535-.607 1.5421-.2794 1.8396-.3157.8318.3886.091.3946-.3278.8075-1.967.4857-2.3072.4614-3.4364.8136-.0425.0304.0486.0607 1.5482.1457.6618.0364h1.621l3.0175.2247.7892.522.4736.6376-.079.4857-1.2142.6193-1.6393-.3886-3.825-.9107-1.3113-.3279h-.1822v.1093l1.0929 1.0686 2.0035 1.8092 2.5075 2.3314.1275.5768-.3218.4554-.34-.0486-2.2039-1.6575-.85-.7468-1.9246-1.621h-.1275v.17l.4432.6496 2.3436 3.5214.1214 1.0807-.17.3521-.6071.2125-.6679-.1214-1.3721-1.9246L14.38 17.959l-1.1414-1.9428-.1397.079-.674 7.2552-.3156.3703-.7286.2793-.6071-.4614-.3218-.7468.3218-1.4753.3886-1.9246.3157-1.53.2853-1.9004.17-.6314-.0121-.0425-.1397.0182-1.4328 1.9672-2.1796 2.9446-1.7243 1.8456-.4128.164-.7164-.3704.0667-.6618.4008-.5889 2.386-3.0357 1.4389-1.882.929-1.0868-.0062-.1579h-.0546l-6.3385 4.1164-1.1293.1457-.4857-.4554.0608-.7467.2307-.2429 1.9064-1.3114Z" />,
  { viewBox: "0 0 24 24" },
);
export const IconCodex = svg(
  <path d="M22.2819 9.8211a5.9847 5.9847 0 0 0-.5157-4.9108 6.0462 6.0462 0 0 0-6.5098-2.9A6.0651 6.0651 0 0 0 4.9807 4.1818a5.9847 5.9847 0 0 0-3.9977 2.9 6.0462 6.0462 0 0 0 .7427 7.0966 5.98 5.98 0 0 0 .511 4.9107 6.051 6.051 0 0 0 6.5146 2.9001A5.9847 5.9847 0 0 0 13.2599 24a6.0557 6.0557 0 0 0 5.7718-4.2058 5.9894 5.9894 0 0 0 3.9977-2.9001 6.0557 6.0557 0 0 0-.7475-7.0729zm-9.022 12.6081a4.4755 4.4755 0 0 1-2.8764-1.0408l.1419-.0804 4.7783-2.7582a.7948.7948 0 0 0 .3927-.6813v-6.7369l2.02 1.1686a.071.071 0 0 1 .038.052v5.5826a4.504 4.504 0 0 1-4.4945 4.4944zm-9.6607-4.1254a4.4708 4.4708 0 0 1-.5346-3.0137l.142.0852 4.783 2.7582a.7712.7712 0 0 0 .7806 0l5.8428-3.3685v2.3324a.0804.0804 0 0 1-.0332.0615L9.74 19.9502a4.4992 4.4992 0 0 1-6.1408-1.6464zM2.3408 7.8956a4.485 4.485 0 0 1 2.3655-1.9728V11.6a.7664.7664 0 0 0 .3879.6765l5.8144 3.3543-2.0201 1.1685a.0757.0757 0 0 1-.071 0l-4.8303-2.7865A4.504 4.504 0 0 1 2.3408 7.872zm16.5963 3.8558L13.1038 8.364 15.1192 7.2a.0757.0757 0 0 1 .071 0l4.8303 2.7913a4.4944 4.4944 0 0 1-.6765 8.1042v-5.6772a.79.79 0 0 0-.407-.667zm2.0107-3.0231l-.142-.0852-4.7735-2.7818a.7759.7759 0 0 0-.7854 0L9.409 9.2297V6.8974a.0662.0662 0 0 1 .0284-.0615l4.8303-2.7866a4.4992 4.4992 0 0 1 6.6802 4.66zM8.3065 12.863l-2.02-1.1638a.0804.0804 0 0 1-.038-.0567V6.0742a4.4992 4.4992 0 0 1 7.3757-3.4537l-.142.0805L8.704 5.459a.7948.7948 0 0 0-.3927.6813zm1.0976-2.3654l2.602-1.4998 2.6069 1.4998v2.9994l-2.5974 1.4997-2.6067-1.4997Z" />,
  { viewBox: "0 0 24 24" },
);

// AgentIcon is the mark of the agent a session, request or activity is with.
export function AgentIcon({ agent, size, className }) {
  const codex = agent === "codex";
  const Icon = codex ? IconCodex : IconClaude;
  return <Icon size={size} className={`agent-icon ${codex ? "codex" : "claude"}${className ? " " + className : ""}`} />;
}
