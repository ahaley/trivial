import { NavLink, Route, Routes } from "react-router-dom";

import { BrandMark } from "./components";
import { Dashboard } from "./pages/Dashboard";
import { SessionPage } from "./pages/Session";
import { SettingsPage } from "./pages/Settings";
import { TopicPage } from "./pages/Topic";
import { TopicsPage } from "./pages/Topics";

export function App() {
  return (
    <div className="shell">
      <header className="topbar">
        <NavLink to="/" className="brand">
          <BrandMark />
          Trivial
        </NavLink>
        <nav className="nav">
          <NavLink to="/" end>
            Topics
          </NavLink>
          <NavLink to="/progress">Progress</NavLink>
          <NavLink to="/settings">Settings</NavLink>
        </nav>
      </header>

      <Routes>
        <Route path="/" element={<TopicsPage />} />
        <Route path="/topics/:id" element={<TopicPage />} />
        <Route path="/sessions/:id" element={<SessionPage />} />
        <Route path="/progress" element={<Dashboard />} />
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="*" element={<NotFound />} />
      </Routes>
    </div>
  );
}

function NotFound() {
  return (
    <main className="narrow">
      <p className="eyebrow">404</p>
      <h1>No such page</h1>
      <p className="lede">
        That address does not match anything here. Head back to your topics.
      </p>
      <NavLink className="btn" to="/">
        Go to topics
      </NavLink>
    </main>
  );
}
