import type { Bot } from "../lib/types";
import { isWorking } from "../lib/events";
const colors: Record<string, string> = {
  lavender: "#b19aff",
  mint: "#5bc9a3",
  peach: "#f6a97c",
  blue: "#73b2f3",
  rose: "#eb86ac",
  amber: "#f2bf59",
  purple: "#b19aff",
  green: "#5bc9a3",
  orange: "#f6a97c",
};
function hash(value: string) {
  let result = 0;
  for (const c of value) result = (result * 31 + c.charCodeAt(0)) >>> 0;
  return result;
}
export default function Avatar({
  bot,
  avatar,
  size = 42,
  status,
}: {
  bot?: Bot;
  avatar?: string;
  size?: number;
  status?: string;
}) {
  const identity = bot?.id || avatar || "chief";
  const seed = hash(identity);
  const color =
    colors[avatar || bot?.avatar || ""] || Object.values(colors)[seed % 6];
  const shape = seed % 4;
  const busy = isWorking(status || bot?.status || "");
  return (
    <span
      className={`bot-avatar ${busy ? "is-active" : ""} ${bot?.chief ? "is-chief" : ""}`}
      style={
        {
          width: size,
          height: size,
          "--avatar-color": color,
        } as React.CSSProperties
      }
      aria-hidden="true"
    >
      <svg viewBox="0 0 48 48" width={size} height={size}>
        <g fill={color}>
          {shape === 0 ? (
            <path d="M9 7Q24 1 39 7L44 31Q40 44 24 45Q8 44 4 31Z" />
          ) : shape === 1 ? (
            <path d="M22 3Q24 1 27 3L42 12Q45 14 45 18L44 34Q44 38 40 40L27 47Q24 49 20 47L7 40Q4 38 4 34L3 18Q3 14 6 12Z" />
          ) : shape === 2 ? (
            <path d="M24 3C13 3 3 12 3 24C3 39 13 44 24 44C38 44 45 35 45 23C45 10 36 3 24 3Z" />
          ) : (
            <path d="M24 3Q27 3 29 7L44 34Q48 43 37 44L11 44Q0 43 4 34L19 7Q21 3 24 3Z" />
          )}
        </g>
        <g
          stroke="#202020"
          strokeWidth="3.6"
          strokeLinecap="round"
          className="avatar-eyes"
        >
          <path d={shape === 3 ? "M18 26l-2 4" : "M17 19l-3 7"} />
          <path d={shape === 3 ? "M29 26l2 4" : "M29 19l3 7"} />
        </g>
        {bot?.chief && (
          <path
            d="m19 7 3 3 4-5 3 5 3-3"
            fill="none"
            stroke="#202020"
            strokeWidth="2.3"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        )}
      </svg>
      {["blocked", "waiting", "failed", "error"].includes(
        status || bot?.status || "",
      ) && (
        <span
          className={`avatar-attention ${status === "failed" ? "is-error" : ""}`}
        />
      )}
    </span>
  );
}
