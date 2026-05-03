import { Route, Routes } from 'react-router-dom';
import { ProjectsListPage } from './ProjectsListPage';
import { ProjectDetailPage } from './ProjectDetailPage';

export default function ProjectsPage() {
  return (
    <Routes>
      <Route index element={<ProjectsListPage />} />
      <Route path=":id" element={<ProjectDetailPage />} />
    </Routes>
  );
}
