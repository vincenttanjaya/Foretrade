/** Join class names, skipping falsy values: cx('a', ok && 'b'). */
export const cx = (...classes: (string | false | null | undefined)[]) => classes.filter(Boolean).join(' ')
