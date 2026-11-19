import SubmissionWizard from "./submission-wizard";

/**
 * Change submission entry (route /changes/new; migration contract §2,
 * wizard ruling §18.34): hosts the create mode of the submission wizard —
 * step 1 collects the review flow, title and description; continuing
 * creates the draft and the journey continues at
 * /changes/drafts/:id (step 2, SQL + review). The page title band is gone
 * (owner ruling 2026-10-24); the wizard carries its own step headings.
 */

export default function ChangesNewPage() {
  return <SubmissionWizard mode="create" />;
}
