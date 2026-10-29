import { ApiError, FieldProblem } from '../../api/problem';
import { describeAccessProblem } from '../../core/http/problem-messages-access';

/** The text of one field problem: the translated message of its code, else the server's English detail. */
export function fieldProblemText(problem: FieldProblem): string {
  return describeAccessProblem(problem.code) ?? problem.detail ?? problem.code;
}

/** The text for the field problem at `pointer` of a failed write, or null. */
export function fieldErrorAt(error: ApiError | null, pointer: string): string | null {
  const problem = error?.fieldErrors.get(pointer);
  return problem ? fieldProblemText(problem) : null;
}
