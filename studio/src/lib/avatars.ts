export interface AvatarStyle {
  id: string;
  name: string;
  base: string;
  light: string;
  shade: string;
  ink: string;
  variant: number;
}

export const avatarStyles: readonly AvatarStyle[] = [
  {
    id: "lavender",
    name: "Lavender",
    base: "#a992f4",
    light: "#ddd4ff",
    shade: "#7d63d2",
    ink: "#211b34",
    variant: 0,
  },
  {
    id: "mint",
    name: "Mint",
    base: "#63caa2",
    light: "#c1edda",
    shade: "#3f9e7a",
    ink: "#102a21",
    variant: 1,
  },
  {
    id: "peach",
    name: "Peach",
    base: "#f29b73",
    light: "#ffd5c1",
    shade: "#d36f4d",
    ink: "#351a14",
    variant: 2,
  },
  {
    id: "blue",
    name: "Blue",
    base: "#67adeb",
    light: "#c7e4ff",
    shade: "#4384c0",
    ink: "#13283a",
    variant: 3,
  },
  {
    id: "rose",
    name: "Rose",
    base: "#e77fa7",
    light: "#fac7da",
    shade: "#c45982",
    ink: "#341622",
    variant: 4,
  },
  {
    id: "amber",
    name: "Amber",
    base: "#e7b44c",
    light: "#ffe3a3",
    shade: "#bd8422",
    ink: "#30220d",
    variant: 5,
  },
] as const;

const aliases: Record<string, string> = {
  purple: "lavender",
  green: "mint",
  orange: "peach",
};

export function avatarHash(value: string): number {
  let result = 0;
  for (const character of value) {
    result = (result * 31 + character.charCodeAt(0)) >>> 0;
  }
  return result;
}

export function resolveAvatarStyle(avatar: string | undefined, identity: string): AvatarStyle {
  const requested = aliases[avatar || ""] || avatar;
  return avatarStyles.find((style) => style.id === requested)
    || avatarStyles[avatarHash(identity) % avatarStyles.length];
}
