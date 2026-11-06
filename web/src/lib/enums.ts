// enumWord is an API enum value as a person reads it: its name in lower case,
// words apart, without its prefix, such as "request changes".
export const enumWord = (names: Record<number, string>, value: number | undefined) => (value ? (names[value] ?? '') : '').toLowerCase().replaceAll('_', ' ')
