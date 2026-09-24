Rails.application.routes.draw do
  # A project's hostnames reach Mission Control only for its maintenance page;
  # nothing else answers on them, before any other route.
  constraints(AppHost) do
    match "/", to: "maintenance_pages#show", via: :all
    match "*path", to: "maintenance_pages#show", via: :all, format: false
  end

  # hooks.<base> answers only the webhook and the ping; everything else there
  # is an empty 404, before any other route.
  constraints(HooksHost) do
    get "ping", to: "pings#show"
    post ":name", to: "webhooks#create", constraints: { name: /[a-z][a-z0-9-]*/ }
    match "/", to: "webhooks#not_found", via: :all
    match "*path", to: "webhooks#not_found", via: :all, format: false
  end

  # Signing in is at /sign-in; the form posts to /session, and signing out deletes it.
  get "sign-in", to: "sessions#new", as: :sign_in
  get "session/new", to: redirect("/sign-in")
  resource :session, only: %i[ create destroy ]
  resource :setup, only: %i[ show create ], controller: "setup"
  namespace :setup do
    resource :cloudflare, only: %i[ show create ], controller: "cloudflare"
    resource :storage, only: %i[ show create ], controller: "storage" do
      post :finish
      get :password
    end
  end

  # Reveal health status on /up that returns 200 if the app boots with no exceptions, otherwise 500.
  # Can be used by load balancers and uptime monitors to verify that the app is live.
  get "up" => "rails/health#show", as: :rails_health_check
  get "ping" => "pings#show"

  namespace :api do
    post "projects/sync", to: "projects#sync"
    get "projects/:name/secrets/:key", to: "secrets#show", constraints: { key: %r{[^/]+} }
    post "projects/:name/deploys", to: "deploys#create"
    patch "deploys/:id", to: "deploys#update"
    post "deploys/:id/snapshot", to: "snapshots#create"
    get "deploys/:id/snapshot", to: "snapshots#show"
    post "deploys/:id/restore_data", to: "restore_data#create"
    get "deploys/:id/restore_data", to: "restore_data#show"
    post "runner/jobs/claim", to: "runner_jobs#claim"

    namespace :v1 do
      get "me", to: "me#show"
      resources :links, only: :create do
        member do
          post :access
          post :read
          post :save
        end
      end
      resource :settings, only: %i[ show update ]
      resources :storage, only: %i[ index update ], param: :name
      resources :projects, only: %i[ index show ], param: :name do
        resources :deploys, only: %i[ index show create ], param: :number
        resources :snapshots, only: :index
        resources :backups, only: %i[ create show ]
        resources :volumes, only: %i[ index update ], param: :name
        resource :backup_target, only: :update
        resource :maintenance, only: :update
        resources :restores, only: :create
        resource :logs, only: :show
        resource :webhook, only: :show do
          post :rotate
        end
        resources :secrets, only: %i[ index update destroy ], param: :key, constraints: { key: %r{[^/]+} } do
          post :generate, on: :member
        end
      end
    end
  end

  # Add project (at /link, so no project name can shadow it).
  get "link", to: "project_links#new", as: :link
  post "link/access", to: "project_links#access", as: :link_access
  post "link/read", to: "project_links#read", as: :link_read
  post "link", to: "project_links#create"
  delete "link", to: "project_links#destroy"

  resources :projects, only: :show, param: :name do
    member do
      post :check
      post :rotate_webhook
    end
    resources :deploys, only: %i[ show create ], param: :number
    resources :backups, only: :create, controller: "project_backups"
    resources :snapshots, only: :index, controller: "project_snapshots"
    resources :volumes, only: :update, param: :name, controller: "project_volumes"
    resource :backup_target, only: :update, controller: "project_backup_targets"
    resources :restores, only: %i[ new create ], controller: "project_restores"
    resource :maintenance, only: :update, controller: "project_maintenance" do
      get :preview
    end
    resources :secrets, only: %i[ update destroy ], param: :key do
      post :generate, on: :member
    end
  end

  # Settings is one page; the old section pages send you to their section.
  get "settings", to: "settings/pages#show", as: :settings
  namespace :settings do
    resource :general, only: %i[ show update ], controller: "general"
    resources :storage_locations, path: "storage", param: :name, only: %i[ index new create show ], controller: "storage" do
      member do
        get :password
        post :acknowledge
        post :default, action: :make_default
      end
    end
    resources :tokens, only: %i[ index create destroy ]
  end

  root "projects#index"
end
