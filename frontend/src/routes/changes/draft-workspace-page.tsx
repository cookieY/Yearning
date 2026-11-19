import SubmissionWizard from "./submission-wizard";

/**
 * AI Precheck Workspace (route /changes/drafts/:id; frontend PRD F4,
 * migration contract §4, wizard ruling §18.34): hosts the draft mode of the
 * submission wizard — it enters at step 2 (SQL editor + explicit review),
 * with step 1 holding the order metadata and step 3 the confirm/submit
 * summary. The audited workspace engine (explicit review, monotonic phase
 * fold, bulk browser, gate dock, submit confirm) lives inside the wizard.
 */

export default function DraftWorkspacePage() {
  return <SubmissionWizard mode="draft" />;
}
