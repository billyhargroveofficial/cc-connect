import type { Bot } from "../lib/types";
import { isWorking } from "../lib/events";
import { resolveAvatarStyle } from "../lib/avatars";

function AvatarArtwork({ variant }: { variant: number }) {
  switch (variant) {
    case 1:
      return (
        <>
          <path className="avatar-ground" d="M17 57.5c7.7 1.7 21.5 1.7 29 0" />
          <path className="avatar-shell" d="M29.5 5.5c13.5-1 24 6.2 27.5 18.2l-1.4 21.6c-.6 8.2-7.1 13.2-15.1 13.2H21c-8.1 0-14.4-5-15.2-13.1L5 27.2C4.4 14.9 14.5 6.7 29.5 5.5Z" />
          <path className="avatar-shade" d="M6 42c8 5.6 16.7 7.8 26.3 7.8 9.3 0 17-2.4 23.5-7.2l-.2 3.3c-.6 7.8-6.9 12.6-14.7 12.6H20.8c-7.7 0-13.8-4.8-14.6-12.6L6 42Z" />
          <path className="avatar-highlight" d="M14 17.6c6.1-6.4 18.4-9 28-5.4 2.7 1 3.4 3.2 1.8 4.8-1 1-2.5 1-4.3.5-7.7-2.3-15.4-.8-21.3 3-2 1.3-3.8 1.1-4.7-.2-.6-.9-.5-1.8.5-2.7Z" />
          <rect className="avatar-visor" x="13" y="23" width="38" height="17" rx="8.5" />
          <rect className="avatar-visor-eye" x="21" y="29" width="7" height="4.5" rx="2.25" />
          <rect className="avatar-visor-eye" x="36" y="29" width="7" height="4.5" rx="2.25" />
        </>
      );
    case 2:
      return (
        <>
          <path className="avatar-ground" d="M16 57.5c8.4 1.7 23.5 1.7 32 0" />
          <path className="avatar-shell" d="M32 5C47 5 58 16.3 58 31.3V44c0 8.1-6.4 14.5-14.5 14.5h-23C12.4 58.5 6 52.1 6 44V31.3C6 16.3 17 5 32 5Z" />
          <path className="avatar-shade" d="M6.2 41c7.1 5.4 15.7 8.2 25.8 8.2 10.2 0 18.9-2.9 25.8-8.4V44c0 8.1-6.3 14.5-14.4 14.5H20.6C12.5 58.5 6 52.1 6 44l.2-3Z" />
          <path className="avatar-highlight" d="M15.2 20.2C19 12.9 26.4 9.5 34.7 10c3.2.2 4.5 2.1 3.4 4-1 1.6-2.7 1.8-5 1.7-5.7-.2-10.5 2.3-13.6 6.7-1.5 2.2-3.3 2.9-4.6 1.7-.9-.9-.8-2.2.3-3.9Z" />
          <circle className="avatar-feature" cx="22.5" cy="31" r="3.4" />
          <circle className="avatar-feature" cx="41.5" cy="31" r="3.4" />
          <path className="avatar-mouth" d="M26.5 41.5c3.2 2.7 7.8 2.7 11 0" />
        </>
      );
    case 3:
      return (
        <>
          <path className="avatar-ground" d="M16 57.5c8.4 1.7 23.5 1.7 32 0" />
          <path className="avatar-shell" d="M31.8 5C47.5 5 59 15.9 59 31.2c0 16.2-10.2 27.3-27.2 27.3C15.4 58.5 5 49.3 5 32 5 16.1 15.8 5 31.8 5Z" />
          <path className="avatar-shade" d="M6.4 40.7c7 5.7 15.9 8.7 26.2 8.7 9.6 0 17.9-2.8 24.8-8.2-3.1 10.7-12.1 17.3-25.6 17.3-13.2 0-22.5-6-25.4-17.8Z" />
          <path className="avatar-highlight" d="M13.5 19.8C18.4 11.9 29 8.5 39 11.1c2.8.7 3.7 2.7 2.2 4.2-1 1.1-2.5 1.1-4.4.7-7.4-1.6-14.6 1-19.2 6.3-1.5 1.8-3.1 2.2-4.2 1.1-.9-.9-.8-2 .1-3.6Z" />
          <path className="avatar-eye-line" d="M18.5 30.5c2.2-2.5 5.2-2.5 7.5 0" />
          <rect className="avatar-feature" x="39" y="27" width="5.5" height="9" rx="2.75" />
          <circle className="avatar-feature" cx="32" cy="42" r="2.2" />
        </>
      );
    case 4:
      return (
        <>
          <path className="avatar-ground" d="M17 57.5c7.7 1.7 21.5 1.7 29 0" />
          <path className="avatar-shell" d="M27.4 6.8c2.6-2.5 6.6-2.5 9.2 0l20.6 19.6c3.2 3 3.2 7.9.1 11L37 57.1c-2.8 2.7-7.2 2.7-10 0L6.6 38.4c-3.5-3.2-3.5-8.4 0-11.6L27.4 6.8Z" />
          <path className="avatar-shade" d="M9.2 40.8c7.5 4.4 15 6.6 22.8 6.6 8.2 0 15.9-2.4 23.3-7.1L37 57.1c-2.8 2.7-7.2 2.7-10 0L9.2 40.8Z" />
          <path className="avatar-highlight" d="M18.7 21.5 28.8 12c1.9-1.8 4.1-1.9 5.2-.4 1 1.3.4 2.7-1.2 4.2l-9.6 8.9c-1.7 1.6-3.3 1.9-4.5.7-1.1-1.1-.9-2.7 0-3.9Z" />
          <rect className="avatar-feature" x="20" y="27" width="6" height="11" rx="3" transform="rotate(-9 23 32.5)" />
          <rect className="avatar-feature" x="38" y="27" width="6" height="11" rx="3" transform="rotate(9 41 32.5)" />
          <path className="avatar-mouth" d="M28 43h8" />
        </>
      );
    case 5:
      return (
        <>
          <path className="avatar-ground" d="M14 57.5c9.3 1.7 26.7 1.7 36 0" />
          <path className="avatar-shell" d="M26.5 8.5c2.5-4.7 8.5-4.7 11 0l22.1 38.4c3.1 5.5-.8 11.6-7 11.6H11.4c-6.2 0-10.1-6.1-7-11.6L26.5 8.5Z" />
          <path className="avatar-shade" d="M7.4 42.1c7.3 4.9 15.4 7.2 24.6 7.2 9.3 0 17.6-2.4 24.7-7.2l2.9 4.8c3.1 5.5-.8 11.6-7 11.6H11.4c-6.2 0-10.1-6.1-7-11.6l3-4.8Z" />
          <path className="avatar-highlight" d="M26.4 17.4c2.8-5.2 4.1-7.2 5.7-7.1 1.7.1 2.2 1.9 1.1 4.3l-4.5 9c-1.1 2.2-3 2.8-4.2 1.4-.9-1-.3-3.3 1.9-7.6Z" />
          <rect className="avatar-visor" x="15" y="31" width="34" height="15" rx="7.5" />
          <circle className="avatar-visor-eye" cx="25" cy="38.5" r="2.5" />
          <circle className="avatar-visor-eye" cx="39" cy="38.5" r="2.5" />
        </>
      );
    default:
      return (
        <>
          <path className="avatar-ground" d="M16 57.5c8.4 1.7 23.5 1.7 32 0" />
          <path className="avatar-shell" d="M20 5.5h24c8.3 0 14.5 6.8 14 15.1l-1.4 23.9c-.5 8-6.9 14-14.9 14H21.2c-8 0-14.4-6.1-14.9-14L5 21C4.5 12.5 11.2 5.5 20 5.5Z" />
          <path className="avatar-shade" d="M6.1 40.5c7.5 5.7 16 8.4 25.9 8.4 9.5 0 17.7-2.6 24.9-7.9l-.2 3.5c-.5 8-6.9 14-14.9 14H21.2c-8 0-14.4-6.1-14.9-14l-.2-4Z" />
          <path className="avatar-highlight" d="M13.8 16.7c6.4-5.5 17.1-7.4 27.9-4.3 2.9.9 3.8 2.8 2.3 4.5-1 1.1-2.5 1.2-4.6.7-7.8-1.9-15.2-.7-21 2.8-2.1 1.3-3.8 1.1-4.8-.2-.8-1.1-.7-2.4.2-3.5Z" />
          <rect className="avatar-feature" x="20" y="25" width="5.5" height="11" rx="2.75" />
          <rect className="avatar-feature" x="38.5" y="25" width="5.5" height="11" rx="2.75" />
          <path className="avatar-mouth" d="M27.5 42c2.8 2.1 6.2 2.1 9 0" />
        </>
      );
  }
}

export default function Avatar({
  bot,
  avatar,
  size = 42,
  status,
  identity,
}: {
  bot?: Bot;
  avatar?: string;
  size?: number;
  status?: string;
  identity?: string;
}) {
  const resolvedIdentity = identity || bot?.id || avatar || "chief";
  const style = resolveAvatarStyle(avatar || bot?.avatar, resolvedIdentity);
  const resolvedStatus = status || bot?.status || "";
  const error = ["failed", "error"].includes(resolvedStatus);
  const attention = ["blocked", "waiting", "interrupted"].includes(resolvedStatus);
  const busy = isWorking(resolvedStatus) && !attention;
  const statusKind = error ? "error" : attention ? "attention" : busy ? "working" : "";
  return (
    <span
      className={`bot-avatar ${busy ? "is-active" : ""} ${bot?.chief ? "is-chief" : ""}`}
      style={
        {
          width: size,
          height: size,
          "--avatar-color": style.base,
          "--avatar-light": style.light,
          "--avatar-shade": style.shade,
          "--avatar-ink": style.ink,
        } as React.CSSProperties
      }
      aria-hidden="true"
    >
      <svg viewBox="0 0 64 64" width={size} height={size}>
        <AvatarArtwork variant={style.variant} />
        {bot?.chief && <path className="avatar-chief-mark" d="m50 8.5 1.6 3.7 3.9 1.6-3.9 1.6-1.6 3.8-1.6-3.8-3.9-1.6 3.9-1.6L50 8.5Z" />}
      </svg>
      {statusKind && <span className={`avatar-status is-${statusKind}`} />}
    </span>
  );
}
