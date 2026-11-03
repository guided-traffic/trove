/**
 * Translated messages of the problem codes of the Bastion registrations and the client setup for a
 * Bastion (R-UI-042, R-UI-045, R-BAS-004, R-BAS-007). `describeProblem` consults this table after the
 * access and CI tables. The codes of the token limit and of the lifetime are in the access table.
 * Returns null for a code this table does not know.
 */
export function describeBastionProblem(code: string | null): string | null {
  switch (code) {
    case 'bastion_base_url_unknown':
      return $localize`:@@problem.bastion.base-url-unknown:This Bastion has not reported a base URL yet, or the one it reported is not an entry of server.additional_base_urls. Add it to that setting on the main installation, and the instructions appear.`;
    default:
      return null;
  }
}
