import { Route, Routes } from 'react-router-dom';
import { WorkspacesListPage } from './WorkspacesListPage';
import { WorkspaceDetailPage } from './WorkspaceDetailPage';

// Router shell for the workspaces module — list at the index, detail
// page keyed on :id. Matches the projects module's two-level layout so
// the dashboard reads with one navigation pattern across features.
export default function WorkspacesPage() {
  return (
    <Routes>
      <Route index element={<WorkspacesListPage />} />
      <Route path=":id" element={<WorkspaceDetailPage />} />
    </Routes>
  );
}
