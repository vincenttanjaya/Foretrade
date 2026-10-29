/** The API's rule for a supplied password (bcrypt's limit): 12..72 bytes, not only whitespace. null = fine. */
export function passwordProblem(pw: string) {
  const bytes = new TextEncoder().encode(pw).length
  if (bytes < 12) return 'Use at least 12 characters.'
  if (bytes > 72) return 'Use at most 72 bytes (fewer characters if it has accents or emoji).'
  if (!pw.trim()) return "A password can't be only spaces."
  return null
}
